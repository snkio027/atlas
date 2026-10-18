package installation

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var serviceAccess = map[string]struct{ Namespace, Service, Port string }{"grafana": {"monitoring", "atlas-monitoring-grafana", "80"}, "prometheus": {"monitoring", "atlas-monitoring-prometheus", "9090"}, "alertmanager": {"monitoring", "atlas-monitoring-alertmanager", "9093"}, "s3": {"object-storage", "seaweedfs", "8333"}}

func (w *Workflow) forward(ctx context.Context, name string) (string, func(), <-chan error, error) {
	s, ok := serviceAccess[name]
	if !ok {
		return "", nil, nil, errors.New("unknown access service")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, filepath.Join(ToolDirectory(w.Config.StateDirectory, w.Product.Tools), "kubectl"), "--kubeconfig", filepath.Join(w.runtimeDir(), ".state/kubeconfig"), "--context", "kind-"+w.Config.Cluster, "-n", s.Namespace, "port-forward", "--address=127.0.0.1", "--pod-running-timeout=60s", "service/"+s.Service, ":"+s.Port)
	return startForward(ctx, cancel, cmd)
}

func startForward(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd) (string, func(), <-chan error, error) {
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		cancel()
		return "", nil, nil, e
	}
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		cancel()
		return "", nil, nil, e
	}
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		scan := bufio.NewScanner(pipe)
		rx := regexp.MustCompile(`^Forwarding from 127\.0\.0\.1:([0-9]+) -> `)
		for scan.Scan() {
			if m := rx.FindStringSubmatch(scan.Text()); len(m) > 1 {
				select {
				case ready <- "http://127.0.0.1:" + m[1]:
				default:
				}
			}
		}
	}()
	go func() { done <- cmd.Wait(); close(done) }()
	stop := func() { cancel(); <-done }
	timer := time.NewTimer(65 * time.Second)
	defer timer.Stop()
	select {
	case address := <-ready:
		return address, stop, done, nil
	case e := <-done:
		cancel()
		return "", nil, nil, fmt.Errorf("local access failed: %v", e)
	case <-ctx.Done():
		stop()
		return "", nil, nil, ctx.Err()
	case <-timer.C:
		stop()
		return "", nil, nil, errors.New("local access did not become ready")
	}
}
func (w *Workflow) webClient() (*http.Client, error) {
	b, e := privateRead(filepath.Join(w.Config.StateDirectory, "development-ca.crt"))
	if e != nil {
		return nil, e
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("invalid installation CA")
	}
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || host != "web.atlas.test" {
			return nil, errors.New("unexpected Web destination")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
	}}}, nil
}

// Access is a foreground localhost connection, terminated on Ctrl-C. Web uses
// an HTTP loopback proxy with certificate-verified HTTPS upstream; it does not
// claim the browser trusts the development CA or modify the system trust store.
func (w *Workflow) Access(ctx context.Context, name string) error {
	if !w.Record.Complete {
		return errors.New("access requires a completed installation")
	}
	if e := VerifyTools(w.Config.StateDirectory, w.Product.Tools); e != nil {
		return e
	}
	if e := w.bindCluster(ctx); e != nil {
		return e
	}
	if name != "web" {
		address, stop, done, e := w.forward(ctx, name)
		if e != nil {
			return e
		}
		defer stop()
		w.message(name + ": " + address + " (loopback only; Ctrl-C closes access)")
		select {
		case <-ctx.Done():
			return nil
		case e := <-done:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("local access connection closed: %v", e)
		}
	}
	client, e := w.webClient()
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	target, _ := url.Parse(fmt.Sprintf("https://web.atlas.test:%d", w.Config.HTTPSPort))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = client.Transport
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Host = target.Host }
	proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, e error) {
		http.Error(rw, "verified Web upstream unavailable", http.StatusBadGateway)
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return e
	}
	w.message("web: http://" + listener.Addr().String() + " (HTTP loopback; upstream TLS verified with this installation CA)")
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		_ = server.Close()
		<-done
		return nil
	case e := <-done:
		return e
	}
}
func (w *Workflow) Summary() string {
	return strings.Join([]string{"Cluster: " + w.Config.Cluster, "GitOps: " + w.Config.Repository + " @ " + w.Record.FullCommit, "Private kubeconfig: " + filepath.Join(w.runtimeDir(), ".state/kubeconfig"), "Credentials (not printed): " + filepath.Join(w.Config.StateDirectory, "credentials.json"), "CA: " + filepath.Join(w.Config.StateDirectory, "development-ca.crt"), "Trust Root backup: " + filepath.Join(w.Config.BackupDirectory, w.Record.InstallID), "Access: atlas-install access --config <your-config.json> --service web|grafana|prometheus|s3", "Verify: atlas-install verify --config <your-config.json>", "No hosts, system trust or default kubeconfig changes were made."}, "\n")
}
