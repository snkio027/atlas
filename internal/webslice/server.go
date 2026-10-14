// Package webslice is the deliberately small business application used by S2.
// It consumes only the public HTTP/S3 Binding contract, never Kubernetes APIs.
package webslice

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Client struct {
	Endpoint, Bucket, Access, Secret string
	HTTP                             *http.Client
}

func NewClientFromEnv() (*Client, error) {
	c := &Client{Endpoint: os.Getenv("ATLAS_S3_ENDPOINT"), Bucket: os.Getenv("ATLAS_S3_BUCKET"), Access: os.Getenv("AWS_ACCESS_KEY_ID"), Secret: os.Getenv("AWS_SECRET_ACCESS_KEY"), HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.Endpoint == "" && c.Bucket == "" && c.Access == "" && c.Secret == "" {
		return nil, nil
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || c.Bucket != "uploads" || c.Access == "" || c.Secret == "" || os.Getenv("ATLAS_S3_REGION") != "us-east-1" || os.Getenv("ATLAS_S3_FORCE_PATH_STYLE") != "true" {
		return nil, errors.New("incomplete/invalid S3 Binding")
	}
	return c, nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}
func Sign(req *http.Request, body []byte, access, secret string, now time.Time) {
	day := now.UTC().Format("20060102")
	date := now.UTC().Format("20060102T150405Z")
	scope := day + "/us-east-1/s3/aws4_request"
	payload := digest(body)
	req.URL.RawQuery = strings.ReplaceAll(req.URL.Query().Encode(), "+", "%20")
	req.Header.Set("X-Amz-Date", date)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	headers := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, "host:" + req.URL.Host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + date + "\n", headers, payload}, "\n")
	key := mac(mac(mac(mac([]byte("AWS4"+secret), day), "us-east-1"), "s3"), "aws4_request")
	signature := hex.EncodeToString(mac(key, "AWS4-HMAC-SHA256\n"+date+"\n"+scope+"\n"+digest([]byte(canonical))))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+access+"/"+scope+", SignedHeaders="+headers+", Signature="+signature)
}
func (c *Client) Request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, e := http.NewRequestWithContext(ctx, method, c.Endpoint+path, bytes.NewReader(body))
	if e != nil {
		return nil, 0, e
	}
	Sign(req, body, c.Access, c.Secret, time.Now())
	res, e := c.HTTP.Do(req)
	if e != nil {
		return nil, 0, errors.New("S3 transport failed")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if len(b) > 1<<20 {
		return nil, res.StatusCode, errors.New("oversized S3 response")
	}
	return b, res.StatusCode, e
}
func (c *Client) RoundTrip(ctx context.Context, body []byte) (string, error) {
	nonce := make([]byte, 16)
	if _, e := rand.Read(nonce); e != nil {
		return "", e
	}
	path := "/" + c.Bucket + "/atlas-s2-probe/" + hex.EncodeToString(nonce)
	if _, code, e := c.Request(ctx, "PUT", path, body); e != nil || code != 200 {
		return "", errors.New("object PUT failed")
	}
	got, code, readErr := c.Request(ctx, "GET", path, nil)
	// A fresh bounded context allows cleanup of this request's unique object even
	// if the caller disconnected. No shared/user object is ever targeted.
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, deleted, deleteErr := c.Request(cleanup, "DELETE", path, nil)
	if readErr != nil || code != 200 || !bytes.Equal(got, body) {
		return "", errors.New("object GET mismatch")
	}
	if deleteErr != nil || deleted != 204 {
		return "", errors.New("probe object cleanup failed")
	}
	return digest(got), nil
}
func (c *Client) PermissionProbe(ctx context.Context) error {
	for _, p := range []string{"/atlas-s2-denied?list-type=2", "/uploads?cors="} {
		if _, status, e := c.Request(ctx, "GET", p, nil); e != nil || status != 403 {
			return errors.New("cross-bucket/bucket-configuration access was not denied")
		}
	}
	return nil
}
func NetworkProbe(ctx context.Context, address string) error {
	// DNS success is checked independently: a broken resolver must not pass as a
	// network isolation proof. Probe only the documented S3 capability endpoint.
	if address != "seaweedfs.object-storage.svc.cluster.local:8333" {
		return errors.New("unreviewed network probe endpoint")
	}
	host, _, _ := net.SplitHostPort(address)
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil || len(ips) == 0 {
		return errors.New("DNS did not resolve; isolation unproven")
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, e := d.DialContext(ctx, "tcp", address)
	if e == nil {
		conn.Close()
		return errors.New("unbound Workload can reach S3")
	}
	var n net.Error
	if !errors.As(e, &n) || !n.Timeout() {
		return errors.New("unexpected connection error; isolation unproven")
	}
	return nil
}
func Handler(client *Client) http.Handler {
	mux := http.NewServeMux()
	var requests atomic.Uint64
	var rounds atomic.Uint64
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ready\n")) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE atlas_web_requests_total counter\natlas_web_requests_total %d\n# TYPE atlas_web_s3_roundtrips_total counter\natlas_web_s3_roundtrips_total %d\n", requests.Load(), rounds.Load())
	})
	mux.HandleFunc("POST /roundtrip", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if client == nil {
			http.Error(w, "S3 Binding unavailable", http.StatusServiceUnavailable)
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if e != nil {
			http.Error(w, "body limit is 64 KiB", http.StatusRequestEntityTooLarge)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		sum, e := client.RoundTrip(ctx, body)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		rounds.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "verified", "sha256": sum, "bytes": len(body), "cleanup": "deleted"})
	})
	return mux
}
func Run(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if len(args) > 0 && args[0] == "probe-network" {
		if len(args) != 1 {
			return errors.New("probe-network takes no arguments")
		}
		return NetworkProbe(ctx, "seaweedfs.object-storage.svc.cluster.local:8333")
	}
	c, e := NewClientFromEnv()
	if e != nil {
		return e
	}
	if len(args) > 0 {
		if len(args) != 1 || args[0] != "probe-permissions" || c == nil {
			return errors.New("unknown/unbound probe")
		}
		return c.PermissionProbe(ctx)
	}
	port, e := strconv.Atoi(os.Getenv("PORT"))
	if e != nil || port < 1024 || port > 65535 {
		return errors.New("PORT must be an unprivileged TCP port")
	}
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: Handler(c), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	return server.ListenAndServe()
}
