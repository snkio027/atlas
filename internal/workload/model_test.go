package workload

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/platform"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T) Intent {
	t.Helper()
	i, e := ReadIntent("../../examples/s2")
	if e != nil {
		t.Fatal(e)
	}
	return i
}
func resolved(t *testing.T, in Intent) *Model {
	t.Helper()
	m, e := Resolve(in, true)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestStrictTypedInput(t *testing.T) {
	valid := JSON(fixture(t).Workloads[0])
	for name, change := range map[string]func([]byte) []byte{
		"unknown": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"port": 8080`), []byte(`"rawPodSpec": {}, "port": 8080`), 1)
		},
		"case": func(b []byte) []byte { return bytes.Replace(b, []byte(`"port"`), []byte(`"Port"`), 1) },
		"duplicate": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"port": 8080`), []byte(`"port": 9000, "port": 8080`), 1)
		},
		"missing": func(b []byte) []byte { return bytes.Replace(b, []byte(`"tls": true`), nil, 1) },
		"null": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"metrics": true`), []byte(`"metrics": null`), 1)
		},
		"fraction": func(b []byte) []byte { return bytes.Replace(b, []byte(`"replicas": 1`), []byte(`"replicas": 1.5`), 1) },
		"trailing": func(b []byte) []byte { return append(b, []byte(` {}`)...) }} {
		t.Run(name, func(t *testing.T) {
			var w Workload
			if StrictDecode(change(valid), &w) == nil {
				t.Fatal("accepted malformed input")
			}
		})
	}
}
func TestResolveRejectsUnauthorizedOrImpossibleIntent(t *testing.T) {
	for name, f := range map[string]func(*Intent){
		"project": func(i *Intent) { i.Workloads[0].Project = "missing" }, "workload": func(i *Intent) { i.Bindings[0].Workload = "missing" }, "grant": func(i *Intent) { i.Project.CapabilityAccess = []Grant{} }, "bucket": func(i *Intent) { i.Bindings[0].Bucket = "other" }, "admin": func(i *Intent) { i.Bindings[0].Access = "admin" }, "namespace": func(i *Intent) { i.Project.Name = "platform-argocd" }, "duplicates": func(i *Intent) { i.Workloads = append(i.Workloads, i.Workloads[0]) }, "binding duplicates": func(i *Intent) { i.Bindings = append(i.Bindings, i.Bindings[0]) }, "quota peak": func(i *Intent) { i.Project.Quota.Pods = 1 }, "limits": func(i *Intent) { i.Workloads[0].Resources.Requests.CPU = 501 }, "floating": func(i *Intent) { i.Workloads[0].Image = "example.invalid/api:latest" }, "tls": func(i *Intent) { i.Workloads[0].Exposure.TLS = false }, "wildcard": func(i *Intent) { i.Workloads[0].Exposure.Hostname = "*.atlas.test" }, "zero": func(i *Intent) { i.Workloads[0].Replicas = 0 }} {
		t.Run(name, func(t *testing.T) {
			i := fixture(t)
			f(&i)
			if _, e := Resolve(i, true); e == nil {
				t.Fatal("accepted invalid intent")
			}
		})
	}
	if _, e := Resolve(fixture(t), false); e == nil {
		t.Fatal("uninstalled capability accepted")
	}
}
func TestUpdateCannotRetireOrRebind(t *testing.T) {
	old := resolved(t, fixture(t))
	for name, f := range map[string]func(*Intent){"remove": func(i *Intent) { i.Bindings = []Binding{} }, "rename": func(i *Intent) { i.Bindings[0].Name = "new" }, "disable metrics": func(i *Intent) { i.Workloads[0].Observability.Metrics = false }, "hostname": func(i *Intent) { i.Workloads[0].Exposure.Hostname = "changed.atlas.test" }, "owner": func(i *Intent) { i.Project.Owner = "new-owner" }} {
		t.Run(name, func(t *testing.T) {
			i := fixture(t)
			f(&i)
			next := resolved(t, i)
			if ValidateUpdate(old, next) == nil {
				t.Fatal("retirement/identity change accepted")
			}
		})
	}
	i := fixture(t)
	i.Workloads[0].Replicas = 2
	i.Workloads[0].Image = strings.Replace(i.Workloads[0].Image, "aaaa", "bbbb", 1)
	if e := ValidateUpdate(old, resolved(t, i)); e != nil {
		t.Fatal(e)
	}
}
func compileFixture(t *testing.T) (CompileContext, *Model) {
	t.Helper()
	p, e := platform.Load("../..", platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		t.Fatal(e)
	}
	all, e := p.BundleFiles()
	if e != nil {
		t.Fatal(e)
	}
	f := Files{}
	for name, b := range all {
		if strings.HasPrefix(name, "gitops/") {
			f[name] = b
		}
	}
	// Model a completed D1 instance without reusing public instance ciphertext.
	f[CredentialsPath] = JSON(Object{"apiVersion": "v1", "kind": "List", "items": []Object{
		syntheticSealed("service-object-storage", "seaweedfs-auth", "seaweedfs_s3_config"),
		syntheticSealed("platform-monitoring", "grafana-admin", "admin-user", "admin-password"),
		syntheticSealed("project-platform-smoke", "s3-client", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"),
	}})
	signal := "gitops/platform/management/argocd-self/overlays/development/signal.json"
	f[signal] = JSON(resource("v1", "ConfigMap", "platform-argocd", "atlas-refactor-adoption-signal", nil))
	model, e := p.ObservationResourceModel()
	if e != nil {
		t.Fatal(e)
	}
	return CompileContext{Base: f, Repository: "https://github.com/snkio027/atlas-refactor.git", Branch: "codex/development-platform", ProductSHA256: strings.Repeat("1", 64), CompilerSHA256: strings.Repeat("2", 64), InstallID: strings.Repeat("3", 32), CertificateSHA256: strings.Repeat("4", 64), HTTPSPort: 8443, ResourceModel: model}, resolved(t, fixture(t))
}
func syntheticSealed(ns, name string, keys ...string) Object {
	data := Object{}
	for _, k := range keys {
		data[k] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 128))
	}
	return Object{"apiVersion": "bitnami.com/v1alpha1", "kind": "SealedSecret", "metadata": Object{"name": name, "namespace": ns}, "spec": Object{"encryptedData": data, "template": Object{"metadata": Object{"name": name, "namespace": ns}, "type": "Opaque"}}}
}
func artifactFixture(c CompileContext, m *Model) *Artifacts {
	a := &Artifacts{Schema: 1, InstallID: c.InstallID, CertificateSHA256: c.CertificateSHA256, IntentSHA256: Digest(JSON(m.Intent)), Provider: syntheticSealed("service-object-storage", "seaweedfs-auth", "seaweedfs_s3_config"), Clients: map[string]Object{}}
	for _, b := range m.Intent.Bindings {
		a.Clients[b.ID()] = syntheticSealed(b.Namespace(), b.Secret(), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")
	}
	return a
}
func TestCompileDeterministicOwnershipAndIsolation(t *testing.T) {
	c, m := compileFixture(t)
	before := platform.BundleDigest(c.Base)
	a, e := Compile(c, m, "infrastructure", nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Compile(c, m, "infrastructure", nil)
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("non deterministic", e)
	}
	if platform.BundleDigest(c.Base) != before {
		t.Fatal("mutated input")
	}
	if _, e = Compile(c, m, "consumer", nil); e == nil {
		t.Fatal("unprepared consumers")
	}
	f, e := Compile(c, m, "consumer", artifactFixture(c, m))
	if e != nil {
		t.Fatal(e)
	}
	ids := map[string]OwnedResource{}
	for _, r := range f.Inventory.Resources {
		if ids[r.Identity].Owner != "" {
			t.Fatal("duplicate")
		}
		ids[r.Identity] = r
	}
	for _, id := range []string{"/Namespace//project-demo", "/ServiceAccount/project-demo/web-api", "networking.k8s.io/NetworkPolicy/project-demo/default-deny"} {
		if ids[id].Owner != m.Intent.Project.AppName() {
			t.Fatal("wrong project owner", id)
		}
	}
	if ids["apps/Deployment/project-demo/web-api"].Owner != m.Intent.Workloads[0].AppName() {
		t.Fatal("wrong workload owner")
	}
	if ids["bitnami.com/SealedSecret/project-demo/"+m.Intent.Bindings[0].Secret()].Owner != "platform-credentials" {
		t.Fatal("wrong secret owner")
	}
	// An unbound peer in the same namespace must not receive credential refs or S3 labels.
	i := fixture(t)
	w := i.Workloads[0]
	w.Name = "unbound"
	w.Exposure.Hostname = "unbound.atlas.test"
	i.Workloads = append(i.Workloads, w)
	m = resolved(t, i)
	f, e = Compile(c, m, "consumer", artifactFixture(c, m))
	if e != nil {
		t.Fatal(e)
	}
	raw := f.Files["gitops/workloads/projects/demo/unbound/resources.json"]
	for _, term := range []string{"AWS_ACCESS_KEY_ID", "atlas.io/s3-binding", "secretKeyRef", "ATLAS_S3_ENDPOINT"} {
		if bytes.Contains(raw, []byte(term)) {
			t.Fatal("unbound workload granted S3", term)
		}
	}
	i.Workloads[0], i.Workloads[1] = i.Workloads[1], i.Workloads[0]
	next := resolved(t, i)
	again, e := Compile(c, next, "consumer", artifactFixture(c, next))
	if e != nil || !reflect.DeepEqual(f, again) {
		t.Fatal("input order changes output", e)
	}
}
func TestCompilationRejectsSharedIdentityAndCipherDrift(t *testing.T) {
	c, m := compileFixture(t)
	a := artifactFixture(c, m)
	meta(a.Clients[m.Intent.Bindings[0].ID()])["namespace"] = "project-platform-smoke"
	if _, e := Compile(c, m, "consumer", a); e == nil {
		t.Fatal("accepted foreign cipher")
	}
	a = artifactFixture(c, m)
	obj(a.Provider["spec"])["template"].(map[string]any)["data"] = Object{"secret": "plaintext"}
	if _, e := Compile(c, m, "consumer", a); e == nil {
		t.Fatal("accepted plaintext")
	}
	as, _ := objects(c.Base[AppsPath])
	as = append(as, clone(as[0]))
	c.Base[AppsPath] = list(as)
	if _, e := Compile(c, m, "infrastructure", nil); e == nil {
		t.Fatal("shared source accepted")
	}
}
func TestIntentPathsAndIdentity(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "platform/projects")
	if e := os.MkdirAll(p, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p, "wrong.json"), JSON(fixture(t).Project), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadIntent(root); e == nil {
		t.Fatal("accepted identity/path mismatch")
	}
	if ShortID("CapabilityBinding", "demo", "web-api-storage") != "5d5f8f616377" {
		t.Fatal("identity algorithm changed")
	}
}
func TestArtifactDoesNotSmuggleJSON(t *testing.T) {
	c, m := compileFixture(t)
	b := JSON(artifactFixture(c, m))
	a, e := DecodeArtifacts(b)
	if e != nil || validateArtifacts(c, m, a) != nil {
		t.Fatal("fixture invalid", e)
	}
	var v any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
}

func TestS2RealKustomizeBuild(t *testing.T) {
	tool := os.Getenv("ATLAS_TEST_KUBECTL")
	if tool == "" {
		t.Skip("task quality supplies the locked kubectl")
	}
	c, m := compileFixture(t)
	for _, phase := range []string{"permissions", "project", "infrastructure", "consumer"} {
		var a *Artifacts
		if phase == "consumer" {
			a = artifactFixture(c, m)
		}
		r, e := Compile(c, m, phase, a)
		if e != nil {
			t.Fatal(e)
		}
		root := t.TempDir()
		dirs := map[string]bool{}
		for name, b := range r.Files {
			if e = os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(root, name), b, 0600); e != nil {
				t.Fatal(e)
			}
			if strings.HasSuffix(name, "/kustomization.yaml") {
				dirs[filepath.Dir(name)] = true
			}
		}
		for dir := range dirs {
			out, e := exec.Command(tool, "kustomize", filepath.Join(root, dir)).CombinedOutput()
			if e != nil {
				t.Fatalf("%s %s: %v\n%s", phase, dir, e, out)
			}
		}
	}
}
func FuzzStrictIntent(f *testing.F) {
	b, e := authored.Read("../../examples/s2/platform/workloads/demo/web-api.yaml")
	if e != nil {
		f.Fatal(e)
	}
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) {
		var v Workload
		if StrictDecode(b, &v) == nil {
			var again Workload
			if e := StrictDecode(JSON(v), &again); e != nil || !reflect.DeepEqual(v, again) {
				t.Fatal("unstable typed boundary", e)
			}
		}
	})
}

func TestCanonicalRuntimeQuantities(t *testing.T) {
	if cpu(2000) != "2" || cpu(125) != "125m" || memory(2048) != "2Gi" || memory(128) != "128Mi" {
		t.Fatal("noncanonical API quantity")
	}
}

func TestUnboundCompilerNeedsNoCredentialMaterial(t *testing.T) {
	c, _ := compileFixture(t)
	i := fixture(t)
	i.Bindings = []Binding{}
	m := resolved(t, i)
	if _, e := Compile(c, m, "consumer", nil); e != nil {
		t.Fatal(e)
	}
}
