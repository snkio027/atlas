package platform

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	return root
}
func resourceFile(t *testing.T, path string) []Object {
	t.Helper()
	b, e := readInput(filepath.Join(testRoot(t), path))
	if e != nil {
		t.Fatal(e)
	}
	objs, e := decodeObjects(b)
	if e != nil {
		t.Fatal(e)
	}
	return objs
}
func clone(o Object) Object { b, _ := json.Marshal(o); var c Object; json.Unmarshal(b, &c); return c }
func TestTenantBoundary(t *testing.T) {
	projects := resourceFile(t, "gitops/platform/management/projects/overlays/development/resources.json")
	var tenant Object
	for _, p := range projects {
		if metadata(p)["name"] == "workload-project" {
			tenant = p
		}
	}
	app := resourceFile(t, "gitops/workloads/applications/overlays/development/resources.json")[0]
	model, err := candidate(t).capabilityResourceModel(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, api, kind, ns string
		allowed             bool
	}{
		{"own service", "v1", "Service", "project-platform-smoke", true},
		{"platform service", "v1", "Service", "argocd", false},
		{"root application", "argoproj.io/v1alpha1", "Application", "project-platform-smoke", false},
		{"role escalation", "rbac.authorization.k8s.io/v1", "RoleBinding", "project-platform-smoke", false},
		{"cluster role", "rbac.authorization.k8s.io/v1", "ClusterRole", "", false},
		{"namespace", "v1", "Namespace", "", false},
		{"network exception", "networking.k8s.io/v1", "NetworkPolicy", "project-platform-smoke", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := Object{"apiVersion": tc.api, "kind": tc.kind, "metadata": Object{"name": "test", "namespace": tc.ns}}
			e := permitted(tenant, app, o, model)
			if (e == nil) != tc.allowed {
				t.Fatalf("permission = %v, expected allowed=%v", e, tc.allowed)
			}
		})
	}
}
func TestProhibitedResources(t *testing.T) {
	for _, kind := range []string{"Secret", "Ingress", "Endpoints", "EndpointSlice", "CiliumNetworkPolicy"} {
		o := Object{"apiVersion": "v1", "kind": kind, "metadata": Object{"name": "bad"}}
		if checkObject(o) == nil {
			t.Errorf("accepted %s", kind)
		}
	}
	app := resourceFile(t, "gitops/workloads/applications/overlays/development/resources.json")[0]
	metadata(app)["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
	if checkObject(app) == nil {
		t.Fatal("accepted cascading finalizer")
	}
}
func TestSchemaRejectsTypoAndInvalidEnum(t *testing.T) {
	s := Object{"type": "object", "properties": Object{"dns": Object{"type": "object", "required": []any{"lookupFamily"}, "properties": Object{"lookupFamily": Object{"type": "string", "enum": []any{"IPv4", "IPv6"}}}}}}
	for _, input := range []Object{{"dns": Object{"lookupFamily": "V4Only"}}, {"dns": Object{"dnsLookupFamily": "IPv4"}}, {"dns": Object{"lookupFamily": true}}} {
		if validateSchema(input, s, "$") == nil {
			t.Fatalf("accepted invalid API shape: %v", input)
		}
	}
	if e := validateSchema(Object{"dns": Object{"lookupFamily": "IPv4"}}, s, "$"); e != nil {
		t.Fatal(e)
	}
}
func TestArtifactFailureIsClosed(t *testing.T) {
	p, e := Load(testRoot(t), Tools{"helm", "kubectl", "yq"})
	if e != nil {
		t.Fatal(e)
	}
	if e = p.VerifyArtifacts(); e != nil {
		t.Fatal(e)
	}
	p.Lock.Artifacts["vendor/platform/missing.tgz"] = Artifact{SHA256: strings.Repeat("0", 64)}
	if e = p.VerifyArtifacts(); e == nil {
		t.Fatal("accepted missing artifact")
	}
	delete(p.Lock.Artifacts, "vendor/platform/missing.tgz")
	for k, a := range p.Lock.Artifacts {
		a.SHA256 = strings.Repeat("0", 64)
		p.Lock.Artifacts[k] = a
		break
	}
	if e = p.VerifyArtifacts(); e == nil {
		t.Fatal("accepted changed artifact")
	}
}
func TestMutableAndHiddenImagesRejected(t *testing.T) {
	p, e := Load(testRoot(t), Tools{"helm", "kubectl", "yq"})
	if e != nil {
		t.Fatal(e)
	}
	for _, input := range []any{Object{"image": "nginx:latest"}, Object{"template": Object{"image": "docker.io/library/busybox:1.38.0"}}, "--helper-image=docker.io/foo/bar:v1@sha256:" + strings.Repeat("0", 64)} {
		if p.images(input) == nil {
			t.Fatalf("accepted unlocked image %v", input)
		}
	}
	if e = p.images(Object{"image": p.Lock.Images["envoyProxy"]}); e != nil {
		t.Fatal(e)
	}
}
func luaLiteral(v any) string {
	switch x := v.(type) {
	case map[string]any:
		var parts []string
		for k, v := range x {
			b, _ := json.Marshal(k)
			parts = append(parts, "["+string(b)+"]="+luaLiteral(v))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		var parts []string
		for _, v := range x {
			parts = append(parts, luaLiteral(v))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case nil:
		return "nil"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
func TestCurrentHealth(t *testing.T) {
	lua := os.Getenv("ATLAS_TEST_LUA")
	if lua == "" {
		t.Skip("set ATLAS_TEST_LUA to run real health scripts")
	}
	b, e := exec.Command(lua, "-v").CombinedOutput()
	if e != nil || !strings.HasPrefix(strings.TrimSpace(string(b)), "Lua 5.5.1 ") {
		t.Fatalf("requires Lua 5.5.1: %s, %v", b, e)
	}
	script := filepath.Join(testRoot(t), inputDir, "health/ready.lua")
	condition := func(name string, generation int, status string) Object {
		return Object{"type": name, "observedGeneration": generation, "status": status}
	}
	cert := Object{"kind": "Certificate", "metadata": Object{"generation": 2}, "status": Object{"conditions": []any{condition("Ready", 2, "True")}}}
	stale := clone(cert)
	mapping(slice(field(stale, "status", "conditions"))[0])["observedGeneration"] = 1
	unknown := clone(cert)
	mapping(slice(field(unknown, "status", "conditions"))[0])["status"] = "Unknown"
	ref := Object{"name": "development", "namespace": "gateway", "sectionName": "https"}
	route := Object{"kind": "HTTPRoute", "metadata": Object{"generation": 2, "namespace": "project-platform-smoke"}, "spec": Object{"parentRefs": []any{ref}}, "status": Object{"parents": []any{Object{"parentRef": ref, "controllerName": "gateway.envoyproxy.io/gatewayclass-controller", "conditions": []any{condition("Accepted", 2, "True"), condition("ResolvedRefs", 2, "True")}}}}}
	otherParent := clone(route)
	mapping(slice(field(otherParent, "status", "parents"))[0])["parentRef"] = Object{"name": "other", "namespace": "gateway", "sectionName": "https"}
	foreign := clone(route)
	mapping(slice(field(foreign, "status", "parents"))[0])["controllerName"] = "other-controller"
	rejected := clone(route)
	mapping(slice(field(mapping(slice(field(rejected, "status", "parents"))[0]), "conditions"))[0])["status"] = "False"
	partial := clone(route)
	mapping(partial["spec"])["parentRefs"] = []any{ref, Object{"name": "another"}}
	gateway := Object{"kind": "Gateway", "metadata": Object{"generation": 2}, "spec": Object{"listeners": []any{Object{"name": "https"}}}, "status": Object{"conditions": []any{condition("Accepted", 2, "True"), condition("Programmed", 2, "True")}}}
	for _, tc := range []struct {
		name string
		o    Object
		want string
	}{{"certificate ready", cert, "Healthy"}, {"stale ready", stale, "Progressing"}, {"unknown ready", unknown, "Progressing"}, {"route ready", route, "Healthy"}, {"wrong parent", otherParent, "Progressing"}, {"wrong controller", foreign, "Progressing"}, {"route rejected", rejected, "Degraded"}, {"one parent missing", partial, "Progressing"}, {"listener absent", gateway, "Progressing"}} {
		t.Run(tc.name, func(t *testing.T) {
			quoted, _ := json.Marshal(script)
			src := "obj=" + luaLiteral(tc.o) + "\nlocal h=assert(loadfile(" + string(quoted) + "))()\nassert(h.status == '" + tc.want + "', h.status)\n"
			cmd := exec.CommandContext(context.Background(), lua, "-")
			cmd.Stdin = strings.NewReader(src)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("health failure: %s, %v", b, e)
			}
		})
	}
}

func TestNamespaceSelectorDoesNotWiden(t *testing.T) {
	selector := Object{"matchLabels": Object{"atlas.io/gateway-access": "development"}}
	if !labelMatch(selector, Object{"atlas.io/gateway-access": "development", "another": "label"}) {
		t.Fatal("authorized namespace rejected")
	}
	for _, labels := range []Object{nil, {}, {"atlas.io/gateway-access": "production"}, {"kubernetes.io/metadata.name": "argocd"}} {
		if labelMatch(selector, labels) {
			t.Fatal("unauthorized namespace accepted")
		}
	}
	if labelMatch(Object{}, Object{}) {
		t.Fatal("empty selector accepted")
	}
}
