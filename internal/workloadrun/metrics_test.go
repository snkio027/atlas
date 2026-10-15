package workloadrun

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atlas-refactor/internal/workload"
)

func TestMetricGate(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		code          int
		pass, pending bool
	}{
		{"all replicas", `{"status":"success","data":{"result":[{"value":[1,"1"]},{"value":[1,"1"]}]}}`, 200, true, false},
		{"missing replica", `{"status":"success","data":{"result":[{"value":[1,"1"]}]}}`, 200, false, true},
		{"down replica", `{"status":"success","data":{"result":[{"value":[1,"1"]},{"value":[1,"0"]}]}}`, 200, false, true},
		{"bad value", `{"status":"success","data":{"result":[{"value":[1,"1"]},{"value":[1]}]}}`, 200, false, true},
		{"query failure", `{"status":"error"}`, 200, false, false},
		{"malformed JSON", `{`, 200, false, false},
		{"HTTP failure", `{}`, 503, false, false},
		{"redirect", `{}`, 302, false, false},
		{"bounded response", strings.Repeat(" ", (4<<20)+1), 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != `up{namespace="project-demo",service="web-api"}` {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if tc.code == 302 {
					w.Header().Set("Location", "http://127.0.0.1:1/")
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := loopbackClient()
			defer client.CloseIdleConnections()
			e := queryMetric(context.Background(), client, server.URL, workload.Workload{Project: "demo", Name: "web-api", Replicas: 2})
			if (e == nil) != tc.pass {
				t.Fatalf("pass=%t: %v", tc.pass, e)
			}
			_, pending := e.(Pending)
			if pending != tc.pending {
				t.Fatalf("pending=%t: %v", tc.pending, e)
			}
		})
	}
}

func TestMetricConnectionFailureIsFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := loopbackClient()
	defer client.CloseIdleConnections()
	e := queryMetric(ctx, client, "http://127.0.0.1:1", workload.Workload{Replicas: 1})
	if e == nil {
		t.Fatal("unavailable Prometheus passed")
	}
	if _, pending := e.(Pending); pending {
		t.Fatal("unknown read became Pending")
	}
}
