package installation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/kubeconfig"
	"github.com/snkio027/atlas/internal/observation"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

// Only D1 notifications use this transport. Observation remains GET-only.
// Structured API outcomes distinguish definite rejection from an unknown write;
// kubectl's human-readable, potentially sensitive stderr is never classified.
type notificationClient struct {
	server, path, digest, clusterUID, version string
	client                                    *http.Client
}

type notificationRejection struct {
	Code   int
	Reason string
}

func (e notificationRejection) Error() string {
	return fmt.Sprintf("notification rejected before persistence (%d/%s)", e.Code, e.Reason)
}

func (w *Workflow) notificationClient(ctx context.Context) (*notificationClient, error) {
	path := filepath.Join(w.runtimeDir(), ".state/kubeconfig")
	raw, err := privateRead(path)
	if err != nil {
		return nil, err
	}
	binding, err := privateRead(path + ".sha256")
	if err != nil || Digest(raw) != string(bytes.TrimSpace(binding)) {
		return nil, errors.New("notification kubeconfig binding changed")
	}
	b, err := w.kube(ctx, nil, "config", "view", "--raw", "--minify", "-o", "json")
	if err != nil {
		return nil, err
	}
	var config kubeconfig.Projection
	if err = observation.Decode(b, &config, false); err != nil {
		return nil, errors.New("invalid notification kubeconfig projection")
	}
	server, transport, err := kubeconfig.Transport(config, "kind-"+w.Config.Cluster)
	if err != nil {
		return nil, err
	}
	// Do not make mutation requests replayable or reuse an idle connection.
	transport.DisableKeepAlives = true
	c := &notificationClient{server: server, path: path, digest: Digest(raw), clusterUID: w.Record.ClusterUID, version: "v" + w.Product.Lock.Kubernetes,
		client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.clusterUID == "" {
		transport.CloseIdleConnections()
		return nil, errors.New("notification requires a bound cluster")
	}
	return c, nil
}

func (c *notificationClient) request(ctx context.Context, method, path string, body []byte) (map[string]any, error) {
	raw, err := privateRead(c.path)
	if err != nil || Digest(raw) != c.digest {
		return nil, errors.New("notification kubeconfig changed")
	}
	var input io.Reader
	if body != nil {
		input = io.NopCloser(bytes.NewReader(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, input)
	if err != nil {
		return nil, errors.New("invalid notification request")
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json-patch+json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("notification request outcome unavailable; no retry")
	}
	defer res.Body.Close()
	const limit = 8 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("notification response incomplete; no retry")
	}
	var obj map[string]any
	if err = Decode(data, &obj); err != nil {
		return nil, errors.New("notification response is not unambiguous JSON; no retry")
	}
	if res.StatusCode == http.StatusOK {
		return obj, nil
	}
	status := obj["kind"] == "Status" && obj["apiVersion"] == "v1" && obj["status"] == "Failure" && obj["code"] == float64(res.StatusCode)
	if method == http.MethodGet && res.StatusCode == http.StatusNotFound && status && obj["reason"] == "NotFound" {
		return nil, nil
	}
	if method == http.MethodPatch && status && ((res.StatusCode == 409 && obj["reason"] == "Conflict") || (res.StatusCode == 422 && obj["reason"] == "Invalid")) {
		return nil, notificationRejection{Code: res.StatusCode, Reason: obj["reason"].(string)}
	}
	return nil, fmt.Errorf("notification API response rejected (%d); body suppressed, no retry", res.StatusCode)
}

func (c *notificationClient) fence(ctx context.Context) error {
	v, err := c.request(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return err
	}
	if v["gitVersion"] != c.version {
		return errors.New("notification API version differs from lock")
	}
	n, err := c.request(ctx, http.MethodGet, "/api/v1/namespaces/kube-system", nil)
	if err != nil {
		return err
	}
	if n["kind"] != "Namespace" || nested(n, "metadata", "name") != "kube-system" || nested(n, "metadata", "uid") != c.clusterUID {
		return errors.New("notification cluster identity changed")
	}
	return nil
}
