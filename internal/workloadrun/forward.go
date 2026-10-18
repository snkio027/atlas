package workloadrun

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// These read paths use the API's port-forward subresource. Service proxying
// originates outside monitoring's ingress boundary and is intentionally denied.
// Neither path changes a resource, policy, or workload credential.
func (w *Workflow) forwardService(ctx context.Context, name string) (string, func(), error) {
	var namespace, service, port string
	switch name {
	case "prometheus":
		namespace, service, port = "monitoring", "atlas-monitoring-prometheus", "9090"
	case "s3":
		namespace, service, port = "object-storage", "seaweedfs", "8333"
	default:
		return "", nil, errors.New("unreviewed forwarding target")
	}
	tool, argv, e := w.kubeCommand("-n", namespace, "port-forward", "--address=127.0.0.1", "--pod-running-timeout=30s", "service/"+service, ":"+port)
	if e != nil {
		return "", nil, e
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(forwardCtx, tool, argv...)
	cmd.Dir = w.Config.StateDirectory
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	return startServiceForward(forwardCtx, cancel, cmd, port)
}

func startServiceForward(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, port string) (string, func(), error) {
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		cancel()
		return "", nil, e
	}
	if e = cmd.Start(); e != nil {
		cancel()
		_ = stdout.Close()
		return "", nil, errors.New("forwarding process could not start")
	}
	ready := make(chan string, 1)
	drained := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(stdout)
		re := regexp.MustCompile(`^Forwarding from 127\.0\.0\.1:([0-9]+) -> ` + regexp.QuoteMeta(port) + `$`)
		for scanner.Scan() {
			m := re.FindStringSubmatch(scanner.Text())
			if len(m) != 2 {
				continue
			}
			p, err := strconv.Atoi(m[1])
			if err != nil || p < 1 || p > 65535 {
				continue
			}
			select {
			case ready <- "http://127.0.0.1:" + m[1]:
			default:
			}
			// Keep draining after readiness, including per-connection messages.
		}
		if scanner.Err() != nil {
			cancel()
		}
	}()
	go func() {
		// Wait must not close StdoutPipe while the scanner still reads it.
		<-drained
		_ = cmd.Wait()
		close(done)
	}()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case address := <-ready:
		if e = ctx.Err(); e == nil {
			return address, stop, nil
		}
	case <-done:
		e = errors.New("forwarding process exited before readiness")
	case <-timer.C:
		e = errors.New("forwarding readiness timed out")
	case <-ctx.Done():
		e = ctx.Err()
	}
	stop()
	return "", nil, e
}

func loopbackClient() *http.Client {
	// No environment proxy, credential chain, or redirect to a remote host.
	return &http.Client{Timeout: 10 * time.Second,
		Transport:     &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
