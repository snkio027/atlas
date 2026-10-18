package workloadrun

import (
	"atlas-refactor/internal/installation"
	"atlas-refactor/internal/workload"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the actual locked kubectl CLI against an isolated API fixture. This
// catches argument parsing failures that mocked successful JSON reads conceal.
func TestKubectlCollectionAndNamedReads(t *testing.T) {
	tool := os.Getenv("ATLAS_TEST_KUBECTL")
	if tool == "" {
		t.Skip("set ATLAS_TEST_KUBECTL; task quality supplies the locked client")
	}
	tool, err := filepath.Abs(tool)
	if err != nil {
		t.Fatal(err)
	}
	const collection = "/apis/argoproj.io/v1alpha1/namespaces/platform-argocd/applications"
	item := Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": Object{"name": "probe", "namespace": "platform-argocd", "uid": "fixture", "managedFields": []any{Object{"manager": "argocd-controller", "operation": "Apply"}}}}
	var mode atomic.Int32
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", r.Method)
			http.Error(w, "read only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result Object
		switch r.URL.Path {
		case "/api":
			result = Object{"kind": "APIVersions", "apiVersion": "v1", "versions": []string{"v1"}}
		case "/api/v1":
			result = Object{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1", "resources": []any{}}
		case "/apis":
			version := Object{"groupVersion": "argoproj.io/v1alpha1", "version": "v1alpha1"}
			result = Object{"kind": "APIGroupList", "apiVersion": "v1", "groups": []any{Object{"name": "argoproj.io", "versions": []any{version}, "preferredVersion": version}}}
		case "/apis/argoproj.io/v1alpha1":
			result = Object{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "argoproj.io/v1alpha1", "resources": []any{Object{"name": "applications", "singularName": "application", "namespaced": true, "kind": "Application", "verbs": []string{"get", "list"}}}}
		case collection, collection + "/probe":
			reads.Add(1)
			switch mode.Load() {
			case 1:
				w.WriteHeader(http.StatusForbidden)
				result = Object{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "Forbidden", "code": 403, "message": "fixture denial"}
			case 2:
				_, _ = w.Write([]byte("invalid JSON"))
				return
			default:
				result = item
				if r.URL.Path == collection {
					result = Object{"kind": "ApplicationList", "apiVersion": "argoproj.io/v1alpha1", "items": []any{item}}
				}
			}
		default:
			t.Errorf("unexpected API path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(workload.JSON(result))
	}))
	defer server.Close()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &Workflow{Config: Config{StateDirectory: dir}, toolsVerified: true}
	w.Install.Config.StateDirectory, w.Install.Config.Cluster = dir, "s2-fixture"
	bin := installation.ToolDirectory(dir, nil)
	kubeconfig := filepath.Join(dir, "runtime/.state/kubeconfig")
	for _, p := range []string{bin, filepath.Dir(kubeconfig)} {
		if err = os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(tool, filepath.Join(bin, "kubectl")); err != nil {
		t.Fatal(err)
	}
	config := workload.JSON(Object{"apiVersion": "v1", "kind": "Config", "clusters": []any{Object{"name": "fixture", "cluster": Object{"server": server.URL}}}, "contexts": []any{Object{"name": "kind-s2-fixture", "context": Object{"cluster": "fixture"}}}})
	for name, data := range map[string][]byte{kubeconfig: config, kubeconfig + ".sha256": []byte(workload.Digest(config))} {
		if err = os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// All inputs are synthetic; neither the default kubeconfig nor a live
	// installation, credential, or tool download participates in this test.
	t.Setenv("HOME", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, name := range []string{"", "probe"} {
		got, err := w.get(ctx, "applications.argoproj.io", "platform-argocd", name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "" {
			items := array(got["items"])
			if len(items) != 1 {
				t.Fatal("collection missing")
			}
			got = mapping(items[0])
		}
		if at(got, "metadata", "uid") != "fixture" || len(array(at(got, "metadata", "managedFields"))) != 1 {
			t.Fatal("named identity or ownership evidence lost")
		}
	}
	for _, failure := range []int32{1, 2} {
		mode.Store(failure)
		if _, err = w.get(ctx, "applications.argoproj.io", "platform-argocd", ""); err == nil {
			t.Fatal("failed collection read accepted")
		}
	}
	if reads.Load() < 4 {
		t.Fatal("client never reached the scoped API")
	}
}
