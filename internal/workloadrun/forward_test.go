package workloadrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"atlas-refactor/internal/workload"
)

// A real child process exercises pipe draining, loopback HTTP, cancellation,
// early exit and reaping without any cluster or shell dependency.
func TestServiceForwardProcess(t *testing.T) {
	if mode := os.Getenv("ATLAS_S2_FORWARD_CHILD"); mode != "" {
		if mode == "exit" {
			os.Exit(9)
		}
		if mode == "wrong" {
			fmt.Println("Forwarding from 0.0.0.0:12345 -> 9090")
			fmt.Println("Forwarding from 127.0.0.1:12345 -> 8333")
			fmt.Println("Forwarding from 127.0.0.1:65536 -> 9090")
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			os.Exit(8)
		}
		fmt.Printf("Forwarding from %s -> 9090\n", listener.Addr())
		// Larger than the OS pipe buffer: reads cannot work if the parent stops
		// draining stdout once it sees readiness (the old S3 helper did this).
		for range 20000 {
			fmt.Println("Handling connection for 12345")
		}
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("query") != `up{namespace="project-demo",service="web-api"}` {
				w.WriteHeader(400)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"result":[{"value":[1,"1"]}]}}`)
		}))
		os.Exit(0)
	}
	for _, mode := range []string{"ready", "exit", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 10 * time.Second
			if mode == "wrong" {
				timeout = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServiceForwardProcess$")
			cmd.Env = append(os.Environ(), "ATLAS_S2_FORWARD_CHILD="+mode)
			stdin, e := cmd.StdinPipe()
			if e != nil {
				t.Fatal(e)
			}
			defer stdin.Close()
			address, stop, e := startServiceForward(ctx, cancel, cmd, "9090")
			if mode != "ready" {
				if e == nil {
					stop()
					t.Fatal("accepted unavailable or mismatched forward")
				}
				if mode == "wrong" && !errors.Is(e, context.DeadlineExceeded) {
					t.Fatal(e)
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				defer stop()
				client := loopbackClient()
				defer client.CloseIdleConnections()
				t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
				t.Setenv("NO_PROXY", "")
				if e = queryMetric(ctx, client, address, workload.Workload{Project: "demo", Name: "web-api", Replicas: 1}); e != nil {
					t.Fatal(e)
				}
				stopped := make(chan struct{})
				go func() { stop(); stop(); close(stopped) }()
				select {
				case <-stopped:
				case <-time.After(2 * time.Second):
					t.Fatal("forward cleanup deadlocked")
				}
			}
			if cmd.ProcessState == nil {
				t.Fatal("forward process was not reaped")
			}
		})
	}
}

func TestServiceForwardRejectsUnreviewedTarget(t *testing.T) {
	w := &Workflow{}
	_, _, e := w.forwardService(context.Background(), "arbitrary-service")
	if e == nil || !strings.Contains(e.Error(), "unreviewed") {
		t.Fatal(e)
	}
}
