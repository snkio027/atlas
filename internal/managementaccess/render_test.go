package managementaccess

import (
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/workload"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Files, *platform.ResourceModel) {
	t.Helper()
	p, err := platform.Load("../..", platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := p.BundleFiles()
	if err != nil {
		t.Fatal(err)
	}
	files := Files{}
	for k, v := range all {
		if strings.HasPrefix(k, "gitops/") {
			files[k] = v
		}
	}
	files["gitops/platform/management/argocd-self/overlays/development/signal.json"] = workload.JSON(Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"name": "atlas-refactor-adoption-signal", "namespace": "argocd"}})
	model, err := p.ObservationResourceModel()
	if err != nil {
		t.Fatal(err)
	}
	return files, model
}
func decode(t *testing.T, b []byte) []Object {
	t.Helper()
	xs, err := platform.DecodeJSONManifests(b)
	if err != nil {
		t.Fatal(err)
	}
	return xs
}
func mustGet(t *testing.T, xs []Object, kind, ns, name string) Object {
	t.Helper()
	o, err := get(xs, kind, ns, name)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func overlay(base Files, phases ...Files) Files {
	out := Files{}
	for k, v := range base {
		out[k] = v
	}
	for _, p := range phases {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

func TestManagementAccessBoundariesAndDeterminism(t *testing.T) {
	base, model := fixture(t)
	digest := platform.BundleDigest(base)
	a, err := Render(base, model, 18443)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(base, model, 18443)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("non-deterministic rendering", err)
	}
	if platform.BundleDigest(base) != digest {
		t.Fatal("source mutated")
	}
	if len(a.Prerequisites) != 2 || len(a.Routes) != 2 || a.Prerequisites[workload.PKIPath] == nil || a.Routes[workload.EdgePath] == nil || a.Routes[workload.MonitorPath] == nil {
		t.Fatal("expanded file mutation scope")
	}
	before, err := workload.InventoryOf(base, model)
	if err != nil {
		t.Fatal(err)
	}
	after, err := workload.InventoryOf(overlay(base, a.Prerequisites, a.Routes), model)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+20 {
		t.Fatalf("expected exactly 5 certificates, 10 routes, 5 policies; delta=%d", len(after)-len(before))
	}
	old := map[string]workload.OwnedResource{}
	for _, v := range before {
		old[v.Identity] = v
	}
	changed := map[string]bool{}
	for _, v := range after {
		if p, exists := old[v.Identity]; exists {
			if p.Owner != v.Owner || p.Path != v.Path {
				t.Fatal("ownership changed")
			}
			if p.SHA256 != v.SHA256 {
				changed[v.Identity] = true
			}
		}
	}
	want := map[string]bool{"argoproj.io/AppProject/argocd/platform-project": true, "gateway.networking.k8s.io/Gateway/gateway/development": true, "apps/Deployment/monitoring/atlas-monitoring-grafana": true, "monitoring.coreos.com/Prometheus/monitoring/atlas-monitoring-prometheus": true, "monitoring.coreos.com/Alertmanager/monitoring/atlas-monitoring-alertmanager": true}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("unexpected existing object delta: %v", changed)
	}
	// Add an unrelated business listener, representing an installed S2 project.
	edge := decode(t, base[workload.EdgePath])
	gw := mustGet(t, edge, "Gateway", "gateway", "development")
	business := clone(obj(arr(at(gw, "spec", "listeners"))[1]))
	business["name"] = "business-https"
	business["hostname"] = "archive.atlas.test"
	obj(gw["spec"])["listeners"] = append(arr(at(gw, "spec", "listeners")), business)
	base[workload.EdgePath] = list(edge)
	kept, err := Render(base, model, 28443)
	if err != nil {
		t.Fatal(err)
	}
	kg := mustGet(t, decode(t, kept.Routes[workload.EdgePath]), "Gateway", "gateway", "development")
	if !reflect.DeepEqual(arr(at(kg, "spec", "listeners"))[2], business) {
		t.Fatal("business listener changed")
	}
}

func TestManagementRoutesTLSMethodsNetworkAndPorts(t *testing.T) {
	base, model := fixture(t)
	r, err := Render(base, model, 18443)
	if err != nil {
		t.Fatal(err)
	}
	edge := decode(t, r.Routes[workload.EdgePath])
	pki := decode(t, r.Prerequisites[workload.PKIPath])
	gw := mustGet(t, edge, "Gateway", "gateway", "development")
	listeners := map[string]Object{}
	for _, l := range arr(at(gw, "spec", "listeners")) {
		listeners[at(obj(l), "name").(string)] = obj(l)
	}
	for _, e := range Endpoints() {
		t.Run(e.Name, func(t *testing.T) {
			n := "management-" + e.Name
			cert := mustGet(t, pki, "Certificate", "gateway", n+"-tls")
			if at(cert, "spec", "issuerRef", "name") != "development-ca" || !reflect.DeepEqual(at(cert, "spec", "dnsNames"), []any{e.Name + ".atlas.test"}) {
				t.Fatal("certificate not bound to exact host and existing issuer")
			}
			for _, proto := range []string{"http", "https"} {
				listener := listeners[n+"-"+proto]
				if at(listener, "allowedRoutes", "namespaces", "from") != "Selector" || !reflect.DeepEqual(at(listener, "allowedRoutes", "namespaces", "selector"), Object{"matchLabels": Object{"kubernetes.io/metadata.name": e.Namespace}}) {
					t.Fatal("listener namespace boundary widened")
				}
				route := mustGet(t, edge, "HTTPRoute", e.Namespace, n+"-"+proto)
				refs := arr(at(route, "spec", "parentRefs"))
				if len(refs) != 1 || at(obj(refs[0]), "sectionName") != n+"-"+proto || at(obj(refs[0]), "namespace") != "gateway" {
					t.Fatal("wrong route attachment")
				}
				rules := arr(at(route, "spec", "rules"))
				if len(rules) != 1 {
					t.Fatal("unexpected fallback rule")
				}
				rule := obj(rules[0])
				matches := arr(rule["matches"])
				if e.ReadOnly {
					if len(matches) != 2 || at(obj(matches[0]), "method") != "GET" || at(obj(matches[1]), "method") != "HEAD" {
						t.Fatal("write method admitted")
					}
				} else if len(matches) != 1 || obj(matches[0])["method"] != nil {
					t.Fatal("authenticated API methods restricted")
				}
				if proto == "http" {
					f := obj(arr(rule["filters"])[0])
					if at(f, "requestRedirect", "port") != float64(18443) || at(f, "requestRedirect", "statusCode") != float64(308) || rule["backendRefs"] != nil {
						t.Fatal("insecure route reaches backend or wrong redirect")
					}
				} else {
					ref := obj(arr(rule["backendRefs"])[0])
					if ref["name"] != e.Service || ref["port"] != float64(e.ServicePort) || ref["namespace"] != nil || ref["weight"] != float64(1) {
						t.Fatal("backend escaped local Service")
					}
					if at(listener, "tls", "mode") != "Terminate" || at(obj(arr(at(listener, "tls", "certificateRefs"))[0]), "name") != n+"-tls" {
						t.Fatal("wrong TLS secret")
					}
				}
			}
			np := mustGet(t, edge, "NetworkPolicy", e.Namespace, n+"-ingress")
			ingress := obj(arr(at(np, "spec", "ingress"))[0])
			peer := obj(arr(ingress["from"])[0])
			if len(arr(ingress["from"])) != 1 || at(peer, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "envoy-gateway-system" || at(peer, "podSelector", "matchLabels", "gateway.envoyproxy.io/owning-gateway-name") != "development" || at(peer, "podSelector", "matchLabels", "gateway.envoyproxy.io/owning-gateway-namespace") != "gateway" {
				t.Fatal("gateway identity widened")
			}
			ports := arr(ingress["ports"])
			if len(ports) != 1 || at(obj(ports[0]), "port") != float64(e.PodPort) {
				t.Fatal("incorrect Pod port or reloader/metrics port exposed")
			}
		})
	}
}

// Regression: Argo waits for earlier-wave health. A route referencing a new
// listener becomes NoMatchingParent if applied before the wave-2 Gateway,
// which prevents Argo from ever reaching that Gateway update.
func TestManagementRoutesWaitForGatewayListeners(t *testing.T) {
	base, model := fixture(t)
	result, err := Render(base, model, 18443)
	if err != nil {
		t.Fatal(err)
	}
	edge := decode(t, result.Routes[workload.EdgePath])
	gateway := mustGet(t, edge, "Gateway", "gateway", "development")
	if at(gateway, "metadata", "annotations", "argocd.argoproj.io/sync-wave") != "2" {
		t.Fatal("Gateway wave changed")
	}
	count := 0
	for _, o := range edge {
		name, _ := at(o, "metadata", "name").(string)
		if o["kind"] == "HTTPRoute" && strings.HasPrefix(name, "management-") {
			count++
			if at(o, "metadata", "annotations", "argocd.argoproj.io/sync-wave") != "3" {
				t.Fatal("route health can block its Gateway prerequisite", name)
			}
		}
	}
	if count != 10 {
		t.Fatal("missing management route", count)
	}
	for _, wave := range []any{nil, "0", "3", "invalid"} {
		xs := decode(t, base[workload.EdgePath])
		gw := mustGet(t, xs, "Gateway", "gateway", "development")
		obj(at(gw, "metadata", "annotations"))["argocd.argoproj.io/sync-wave"] = wave
		drifted := overlay(base, Files{workload.EdgePath: list(xs)})
		if _, err := Render(drifted, model, 18443); err == nil {
			t.Fatal("accepted unreviewed Gateway wave", wave)
		}
	}
}

func TestManagementRejectsUnsafeOrIncompatibleBase(t *testing.T) {
	for _, name := range []string{"missing-service", "wrong-selector", "wrong-port", "wrong-pod-port", "argo-tls", "duplicate-identity", "permission", "schema", "existing-listener"} {
		t.Run(name, func(t *testing.T) {
			base, model := fixture(t)
			switch name {
			case "missing-service", "wrong-selector", "wrong-port":
				xs := decode(t, base[workload.StoragePath])
				o := mustGet(t, xs, "Service", "object-storage", "seaweedfs")
				if name == "missing-service" {
					obj(o["metadata"])["name"] = "different"
				} else if name == "wrong-selector" {
					obj(at(o, "spec", "selector"))["app.kubernetes.io/name"] = "other"
				} else {
					obj(arr(at(o, "spec", "ports"))[0])["port"] = 9999
				}
				base[workload.StoragePath] = list(xs)
			case "wrong-pod-port":
				xs := decode(t, base[workload.StoragePath])
				o := mustGet(t, xs, "StatefulSet", "object-storage", "seaweedfs")
				c := obj(arr(at(o, "spec", "template", "spec", "containers"))[0])
				for _, p := range arr(c["ports"]) {
					if at(obj(p), "name") == "s3" {
						obj(p)["containerPort"] = 9999
					}
				}
				base[workload.StoragePath] = list(xs)
			case "argo-tls":
				path := "gitops/platform/management/argocd-self/overlays/development/rendered.yaml"
				xs := decode(t, base[path])
				o := mustGet(t, xs, "ConfigMap", "argocd", "argocd-cmd-params-cm")
				obj(o["data"])["server.insecure"] = "false"
				base[path] = list(xs)
			case "duplicate-identity":
				xs := decode(t, base[workload.PKIPath])
				xs = append(xs, resource("cert-manager.io/v1", "Certificate", "gateway", "management-grafana-tls", clone(obj(xs[len(xs)-1]["spec"]))))
				base[workload.PKIPath] = list(xs)
			case "permission":
				xs := decode(t, base[workload.ProjectsPath])
				p := mustGet(t, xs, "AppProject", "argocd", "platform-project")
				rules := arr(at(p, "spec", "namespaceResourceWhitelist"))
				out := []any{}
				for _, v := range rules {
					if at(obj(v), "kind") != "NetworkPolicy" {
						out = append(out, v)
					}
				}
				obj(p["spec"])["namespaceResourceWhitelist"] = out
				base[workload.ProjectsPath] = list(xs)
			case "schema":
				typ, _ := model.Resolve(Object{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute"})
				obj(at(typ.Schema, "properties", "spec", "properties"))["future"] = Object{"type": "string", "default": "unsafe"}
			case "existing-listener":
				r, err := Render(base, model, 18443)
				if err != nil {
					t.Fatal(err)
				}
				base = overlay(base, r.Prerequisites, r.Routes)
			}
			if _, err := Render(base, model, 18443); err == nil {
				t.Fatal("accepted incompatible base")
			}
		})
	}
}

func TestManagementRealKustomize(t *testing.T) {
	tool := os.Getenv("ATLAS_TEST_KUBECTL")
	if tool == "" {
		t.Skip("set ATLAS_TEST_KUBECTL for real offline Kustomize")
	}
	base, model := fixture(t)
	r, err := Render(base, model, 18443)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Files{r.Prerequisites, r.Routes} {
		base = overlay(base, phase)
		dir := filepath.Join(t.TempDir(), "tree")
		if err := WriteNew(dir, base); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{workload.PKIPath, workload.EdgePath, workload.MonitorPath} {
			out, err := exec.Command(tool, "kustomize", filepath.Join(dir, filepath.Dir(path))).CombinedOutput()
			if err != nil {
				t.Fatalf("Kustomize: %v %s", err, out)
			}
			if !bytes.Contains(out, []byte("apiVersion:")) {
				t.Fatal("empty render")
			}
		}
	}
}

func TestManagementFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	gitops := filepath.Join(dir, "gitops")
	if err := os.Mkdir(gitops, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(gitops, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTree(dir); err == nil {
		t.Fatal("read symlink")
	}
	if err := WriteNew(dir, Files{"gitops/a": []byte("x")}); err == nil {
		t.Fatal("overwrote existing directory")
	}
	if err := WriteNew(filepath.Join(dir, "new"), Files{"../outside": []byte("x")}); err == nil {
		t.Fatal("accepted escaping output")
	}
}
