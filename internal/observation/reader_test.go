package observation

import (
	"context"
	"github.com/snkio027/atlas/internal/platform"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAPIReadOnlyHTTPFences(t *testing.T) {
	root, _ := filepath.Abs("../..")
	p, e := platform.Load(root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		t.Fatal(e)
	}
	model, e := p.ObservationResourceModel()
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	file := filepath.Join(dir, "kubeconfig")
	if e = os.WriteFile(file, []byte("bound"), 0600); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	requests := []string{}
	mode := "ok"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != "GET" {
			t.Error("non-GET request")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1" {
			w.Write(Bytes(Object{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{Object{"name": "namespaces", "kind": "Namespace", "namespaced": false, "verbs": []any{"get", "list"}}}}))
			return
		}
		if mode == "unavailable" {
			w.WriteHeader(503)
			return
		}
		if mode == "missing" {
			w.WriteHeader(404)
			return
		}
		if mode == "redirect" {
			http.Redirect(w, r, "/unexpected", 302)
			return
		}
		o := Object{"apiVersion": "v1", "kind": "Namespace", "metadata": Object{"name": "kube-system", "uid": "cluster", "resourceVersion": "1"}}
		if mode == "wrong-identity" {
			Map(o["metadata"])["name"] = "another"
		}
		w.Write(Bytes(o))
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	reader := &APIReader{target: Target{KubeconfigSHA256: SHA([]byte("bound"))}, kubeconfig: file, server: server.URL, client: client, model: model, discovery: map[string]map[string]string{}}
	ref := Ref{APIVersion: "v1", Kind: "Namespace", Name: "kube-system"}
	if _, e = reader.Read(context.Background(), ref); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	mode = "missing"
	mu.Unlock()
	if o, e := reader.Read(context.Background(), ref); e != nil || o != nil {
		t.Fatal("confirmed absence")
	}
	for _, bad := range []string{"unavailable", "wrong-identity", "redirect"} {
		mu.Lock()
		mode = bad
		mu.Unlock()
		if _, e = reader.Read(context.Background(), ref); e == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	mu.Lock()
	before := len(requests)
	mu.Unlock()
	if _, e = reader.Read(context.Background(), Ref{APIVersion: "v1", Kind: "Secret", Namespace: "argocd", Name: "key"}); e == nil || requestCount(&mu, requests) != before {
		t.Fatal("secret reached API")
	}
	if e = os.WriteFile(file, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = reader.Read(context.Background(), ref); e == nil || requestCount(&mu, requests) != before {
		t.Fatal("changed credential binding reached API")
	}
}

func requestCount(mu *sync.Mutex, values []string) int {
	mu.Lock()
	defer mu.Unlock()
	return len(values)
}

func TestTypedInventoryNormalization(t *testing.T) {
	root, _ := filepath.Abs("../..")
	p, e := platform.Load(root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		t.Fatal(e)
	}
	model, e := p.ObservationResourceModel()
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	file := filepath.Join(dir, "kubeconfig")
	if e = os.WriteFile(file, []byte("bound"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, ref := range []Ref{
		{APIVersion: "v1", Kind: "Node"},
		{APIVersion: "v1", Kind: "Pod", Namespace: "workload-web"},
		{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "workload-web"},
		{APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Namespace: "argocd"},
	} {
		t.Run(ref.Kind, func(t *testing.T) {
			member := Object{"metadata": Object{"name": "probe", "uid": "stable", "resourceVersion": "7"}}
			if ref.Namespace != "" {
				Map(member["metadata"])["namespace"] = ref.Namespace
			}
			base := Object{"apiVersion": ref.APIVersion, "kind": ref.Kind + "List", "metadata": Object{"resourceVersion": "9"}, "items": []any{member}}
			cases := []struct {
				name   string
				change func(Object)
				valid  bool
			}{
				{"omitted type metadata", func(Object) {}, true},
				{"explicit type metadata", func(o Object) { m := Map(Slice(o["items"])[0]); m["apiVersion"], m["kind"] = ref.APIVersion, ref.Kind }, true},
				{"empty inventory", func(o Object) { o["items"] = []any{} }, true},
				{"wrong list version", func(o Object) { o["apiVersion"] = "v99" }, false},
				{"untyped list", func(o Object) { o["kind"] = "List" }, false},
				{"missing list version", func(o Object) { delete(o, "apiVersion") }, false},
				{"missing list resource version", func(o Object) { delete(Map(o["metadata"]), "resourceVersion") }, false},
				{"pagination", func(o Object) { Map(o["metadata"])["continue"] = "next" }, false},
				{"null continuation", func(o Object) { Map(o["metadata"])["continue"] = nil }, false},
				{"null items", func(o Object) { o["items"] = nil }, false},
				{"null item", func(o Object) { o["items"] = []any{nil} }, false},
				{"scalar item", func(o Object) { o["items"] = []any{"invalid"} }, false},
				{"duplicate identity", func(o Object) { o["items"] = append(Slice(o["items"]), Clone(member)) }, false},
				{"namespace mismatch", func(o Object) { Map(At(Map(Slice(o["items"])[0]), "metadata"))["namespace"] = "wrong" }, false},
				{"invalid name", func(o Object) { Map(At(Map(Slice(o["items"])[0]), "metadata"))["name"] = "../escape" }, false},
			}
			for _, field := range []string{"apiVersion", "kind"} {
				for _, value := range []any{"", nil, 1, []any{}, "Conflicting"} {
					cases = append(cases, struct {
						name   string
						change func(Object)
						valid  bool
					}{
						field + "=" + string(Bytes(value)), func(o Object) { Map(Slice(o["items"])[0])[field] = value }, false,
					})
				}
				cases = append(cases, struct {
					name   string
					change func(Object)
					valid  bool
				}{
					"only " + field, func(o Object) {
						Map(Slice(o["items"])[0])[field] = map[string]string{"apiVersion": ref.APIVersion, "kind": ref.Kind}[field]
					}, true,
				})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					list := Clone(base)
					tc.change(list)
					prefix := "/api/" + ref.APIVersion
					if ref.APIVersion != "v1" {
						prefix = "/apis/" + ref.APIVersion
					}
					path := prefix
					if ref.Namespace != "" {
						path += "/namespaces/" + ref.Namespace
					}
					path += "/probes"
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != "GET" {
							t.Error("inventory attempted a write")
						}
						switch r.URL.Path {
						case prefix:
							w.Write(Bytes(Object{"kind": "APIResourceList", "groupVersion": ref.APIVersion, "resources": []any{Object{"name": "probes", "kind": ref.Kind, "namespaced": ref.Namespace != "", "verbs": []any{"get", "list"}}}}))
						case path:
							w.Write(Bytes(list))
						default:
							t.Errorf("unexpected path %s", r.URL.Path)
							w.WriteHeader(404)
						}
					}))
					defer server.Close()
					reader := &APIReader{target: Target{KubeconfigSHA256: SHA([]byte("bound"))}, kubeconfig: file, server: server.URL, client: server.Client(), model: model, discovery: map[string]map[string]string{}}
					got, err := reader.List(context.Background(), ref)
					if !tc.valid {
						if err == nil || got != nil {
							t.Fatal("invalid inventory normalized into evidence")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for _, o := range got {
						want := ref
						want.Name = "probe"
						if Reference(o) != want || String(At(o, "metadata", "uid")) != "stable" || String(At(o, "metadata", "resourceVersion")) != "7" {
							t.Fatalf("canonical identity changed: %v", o)
						}
					}
				})
			}
		})
	}
}

func TestTypedInventoryCannotBypassDiscoveryOrScope(t *testing.T) {
	root, _ := filepath.Abs("../..")
	p, e := platform.Load(root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		t.Fatal(e)
	}
	model, e := p.ObservationResourceModel()
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	file := filepath.Join(dir, "kubeconfig")
	if e = os.WriteFile(file, []byte("bound"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name          string
		ref           Ref
		discoveryKind string
		namespaced    bool
	}{
		{"cluster scope with namespace", Ref{APIVersion: "v1", Kind: "Node", Namespace: "wrong"}, "Node", false},
		{"namespaced scope without namespace", Ref{APIVersion: "v1", Kind: "Pod"}, "Pod", true},
		{"discovery scope conflict", Ref{APIVersion: "v1", Kind: "Node"}, "Node", true},
		{"GVK not served", Ref{APIVersion: "v1", Kind: "Node"}, "Pod", true},
		{"GVK not locked", Ref{APIVersion: "v1", Kind: "Unknown"}, "Unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1" {
					t.Error("unverified type reached inventory endpoint")
				}
				w.Write(Bytes(Object{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{Object{"name": "probes", "kind": tc.discoveryKind, "namespaced": tc.namespaced, "verbs": []any{"get", "list"}}}}))
			}))
			defer server.Close()
			reader := &APIReader{target: Target{KubeconfigSHA256: SHA([]byte("bound"))}, kubeconfig: file, server: server.URL, client: server.Client(), model: model, discovery: map[string]map[string]string{}}
			if got, e := reader.List(context.Background(), tc.ref); e == nil || got != nil {
				t.Fatal("unverified scope/GVK accepted")
			}
		})
	}
}
