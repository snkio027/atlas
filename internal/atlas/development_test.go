package atlas

import (
	"archive/zip"
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/platform"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func developmentFixture(t *testing.T) (*App, *simulator) {
	t.Helper()
	a, s := fixture(t)
	for _, dir := range []string{"platform/development", "gitops/root", "gitops/platform", "gitops/workloads", "vendor/platform"} {
		source := filepath.Join("../..", dir)
		e := filepath.WalkDir(source, func(path string, entry os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if entry.IsDir() {
				return nil
			}
			rel, e := filepath.Rel("../..", path)
			if e != nil {
				return e
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			dst := filepath.Join(a.Root, rel)
			if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
				return e
			}
			return os.WriteFile(dst, b, 0600)
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	// Historical tests use immutable old inputs, never the new product layout.
	for _, dir := range []string{"platform/development", "gitops"} {
		if e := os.RemoveAll(filepath.Join(a.Root, dir)); e != nil {
			t.Fatal(e)
		}
	}
	z, e := zip.OpenReader("testdata/development-legacy.zip")
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	for _, f := range z.File {
		if !filepath.IsLocal(f.Name) {
			t.Fatal("unsafe fixture path")
		}
		in, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(in)
		in.Close()
		if e != nil {
			t.Fatal(e)
		}
		dest := filepath.Join(a.Root, f.Name)
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	// Bootstrap simulations start from the frozen core inventory, independent
	// of the capabilities currently selected for the real development cluster.
	selection := "platform/development/capabilities/enabled.json"
	if e := os.WriteFile(filepath.Join(a.Root, selection), []byte(`{"schema":1,"capabilities":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := platform.Load(a.Root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		t.Fatal(e)
	}
	core, e := p.CapabilityActivation(nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Write(core); e != nil {
		t.Fatal(e)
	}
	a.Config = Config{3, "atlas-refactor-test-dev02", "https://github.com/snkio027/atlas-refactor.git", "codex/development-platform", "gitops/root/overlays/development", "orbstack", 30}
	prepareFixtureArchives(t, a)
	files, e := a.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = WriteFiles(a.Root, ".", files); e != nil {
		t.Fatal(e)
	}
	return a, s
}
func ciliumFixtureObjects() []Object {
	cm := object("ConfigMap", "kube-system", "cilium-config")
	cm["data"] = map[string]string{"enable-ipv4": "true"}
	ds := object("DaemonSet", "kube-system", "cilium")
	ds["apiVersion"] = "apps/v1"
	ds["spec"] = Object{"selector": Object{"matchLabels": Object{"k8s-app": "cilium"}}}
	role := object("ClusterRole", "", "cilium")
	role["apiVersion"] = "rbac.authorization.k8s.io/v1"
	return []Object{cm, ds, role}
}
func (s *simulator) requestSeed(input []byte) []Object {
	if s.app.development != nil && bytes.Contains(input, []byte(`"name": "cilium-config"`)) {
		return ciliumFixtureObjects()
	}
	return s.seedObjects()
}
func (s *simulator) developmentGitops() {
	a := s.app
	root := s.objects[key("Application", "argocd", "atlas-refactor-root")]
	root["status"] = Object{"sync": Object{"status": "Synced", "revision": strings.Repeat("a", 40)}, "health": Object{"status": "Healthy"}}
	for name, want := range a.development.apps {
		var app Object
		_ = decode(jsonBytes(want), &app)
		app["metadata"] = Object(app["metadata"].(map[string]any))
		app["status"] = Object{"sync": Object{"status": "Synced", "revision": strings.Repeat("a", 40)}, "health": Object{"status": "Healthy"}}
		s.put(app)
		var objects []Object
		namespace := "argocd"
		if name == "argocd-self" {
			objects = s.seedObjects()
		} else if name == "cilium" {
			objects = ciliumFixtureObjects()
			namespace = "kube-system"
		} else {
			continue
		}
		inventory := []Object{}
		results := []Object{}
		for _, obj := range objects {
			meta := obj["metadata"].(Object)
			var projected Live
			_ = decode(jsonBytes(obj), &projected)
			if projected.Metadata.Namespace == "" {
				projected.Metadata.Namespace = namespace
			}
			meta["annotations"] = map[string]string{"argocd.argoproj.io/tracking-id": name + ":" + seedKey(&projected)}
			meta["managedFields"] = []Object{{"manager": "argocd-controller", "operation": "Apply", "fieldsV1": Object{"f:spec": Object{}}}}
			if obj["kind"] == "CustomResourceDefinition" {
				delete(meta, "annotations")
				inventory = append(inventory, Object{"group": "apiextensions.k8s.io", "version": "v1", "kind": obj["kind"], "name": meta["name"], "status": "Synced"})
				results = append(results, Object{"group": "apiextensions.k8s.io", "version": "v1", "kind": obj["kind"], "name": meta["name"], "namespace": namespace, "status": "Synced", "syncPhase": "Sync"})
			}
			s.put(obj)
		}
		if name == "argocd-self" {
			inventory = append(inventory, Object{"group": "", "kind": "ConfigMap", "namespace": "argocd", "name": "atlas-refactor-adoption-signal"})
		}
		app["status"].(Object)["resources"] = inventory
		app["status"].(Object)["operationState"] = Object{"phase": "Succeeded", "syncResult": Object{"revision": strings.Repeat("a", 40), "resources": results}}
	}
	signal := configMap("argocd", "atlas-refactor-adoption-signal", map[string]string{"fingerprint": a.fingerprint()})
	signal["metadata"].(Object)["annotations"] = map[string]string{"argocd.argoproj.io/tracking-id": "argocd-self:/ConfigMap:argocd/atlas-refactor-adoption-signal"}
	s.put(signal)
}
func TestDevelopmentCiliumBeforeArgoAndIdempotency(t *testing.T) {
	a, s := developmentFixture(t)
	apply(t, a)
	effects := strings.Join(s.effects, "|")
	cni := strings.Index(effects, "cilium:apply")
	argo := strings.Index(effects, "seed:apply")
	if cni < 0 || argo < cni {
		t.Fatal("Argo installed before CNI", effects)
	}
	if r := a.Status(context.Background()); r.State != Adopted {
		t.Fatal(r)
	}
	n := len(s.effects)
	apply(t, a)
	if len(s.effects) != n {
		t.Fatal("adopted development reapply mutated cluster")
	}
}
func TestDevelopmentInterruptionOnlyCompletesReceipt(t *testing.T) {
	a, s := developmentFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelAfterRoot = cancel
	if e := a.Apply(ctx, a.Config.Cluster, true); e == nil {
		t.Fatal("expected interrupted handoff")
	}
	n := len(s.effects)
	s.cancelAfterRoot = nil
	apply(t, a)
	if strings.Join(s.effects[n:], ",") != "create:atlas-refactor-receipt" {
		t.Fatal("Seed authority resumed", s.effects[n:])
	}
}
func TestDevelopmentSeedOwnershipRemainsInAuthorityGate(t *testing.T) {
	for _, damage := range []string{"cilium-tracking", "cilium-manager"} {
		t.Run(damage, func(t *testing.T) {
			a, s := developmentFixture(t)
			apply(t, a)
			n := len(s.effects)
			switch damage {
			case "cilium-tracking":
				delete(s.objects[key("DaemonSet", "kube-system", "cilium")]["metadata"].(Object), "annotations")
			case "cilium-manager":
				delete(s.objects[key("DaemonSet", "kube-system", "cilium")]["metadata"].(Object), "managedFields")
			}
			if r := a.Status(context.Background()); r.State != Degraded {
				t.Fatal("false development adoption", r)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if e := a.Apply(ctx, a.Config.Cluster, true); e == nil {
				t.Fatal("accepted damaged handoff")
			}
			if len(s.effects) != n {
				t.Fatal("Seed authority restored")
			}
		})
	}
}
func TestHistoricalDevelopmentProfileRejectsUpgradedDependencies(t *testing.T) {
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	c, l, e := Load(root, "profiles/development.json")
	if e != nil {
		t.Fatal(e)
	}
	a := &App{Root: root, Config: c, Lock: l}
	if _, e := a.Render(context.Background()); e == nil {
		t.Fatal("current templates accepted as the historical schema-3 profile")
	}
	// Source templates no longer carry author ciphertext, so activation can
	// reject before the frozen-input check. Exercise that independent boundary
	// directly without adding instance credentials to this fixture.
	p, e := platform.Load(root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		t.Fatal(e)
	}
	files, e := p.BundleFiles()
	if e != nil {
		t.Fatal(e)
	}
	if _, e := a.validateDevelopmentBaseline(files, c.Schema); e == nil || !strings.Contains(e.Error(), "frozen instantiation contract changed: platform/development/") {
		t.Fatalf("upgraded inputs must not reuse the historical schema-3 identity: %v", e)
	}
}

func TestDevelopmentKindExposureFailsClosed(t *testing.T) {
	data, err := authored.Read("../../platform/development/bootstrap/kind.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, modified := range [][]byte{
		bytes.ReplaceAll(data, []byte("127.0.0.1"), []byte("0.0.0.0")),
		bytes.Replace(data, []byte(`"disableDefaultCNI": true`), []byte(`"disableDefaultCNI": false`), 1),
		bytes.Replace(data, []byte(`"hostPort": 8080`), []byte(`"hostPort": 80`), 1),
		[]byte(`{"nodes":null}`),
	} {
		if validateDevelopmentKind(modified) == nil {
			t.Fatal("unsafe substrate accepted")
		}
	}
}

func TestDevelopmentGitOpsChangeKeepsBootstrapIdentity(t *testing.T) {
	a, _ := developmentFixture(t)
	before, err := a.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Root, "gitops/workloads/web-smoke/overlays/development/resources.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte("Atlas development web:"), []byte("Updated development web:"), 1)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	next := &App{Root: a.Root, Config: a.Config, Lock: a.Lock, Runner: a.Runner, fixtureSnapshotDigest: a.fixtureSnapshotDigest}
	after, err := next.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before[developmentSignal], after[developmentSignal]) {
		t.Fatal("GitOps leaf change rewrote the immutable Bootstrap signal")
	}
	b, err = os.ReadFile(filepath.Join(a.Root, ciliumSeed))
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	for _, path := range []string{ciliumSeed, "gitops/platform/networking/cilium/overlays/development/rendered.yaml"} {
		if err = os.WriteFile(filepath.Join(a.Root, path), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	next = &App{Root: a.Root, Config: a.Config, Lock: a.Lock, Runner: a.Runner, fixtureSnapshotDigest: a.fixtureSnapshotDigest}
	_, err = next.Render(context.Background())
	if err == nil {
		t.Fatal("changed Seed contract was not rejected")
	}
}

func TestDevelopmentCapabilityExtensionPreservesIdentityAndAuthority(t *testing.T) {
	a, s := developmentFixture(t)
	apply(t, a)
	before, err := a.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := len(s.effects)
	p, err := platform.Load(a.Root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if err != nil {
		t.Fatal(err)
	}
	names, err := p.ResolveCapabilities([]string{"secrets-foundation"})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := p.CapabilityActivation(names)
	if err != nil {
		t.Fatal(err)
	}
	projection["platform/development/capabilities/enabled.json"] = []byte(`{"schema":1,"capabilities":["secrets-foundation"]}`)
	if err = p.Write(projection); err != nil {
		t.Fatal(err)
	}
	next := &App{Root: a.Root, Config: a.Config, Lock: a.Lock, Runner: a.Runner, fixtureSnapshotDigest: a.fixtureSnapshotDigest}
	s.app = next
	after, err := next.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before[developmentSignal], after[developmentSignal]) {
		t.Fatal("extension changed initial Signal")
	}
	if got := next.Status(context.Background()); got.State != Adopted {
		t.Fatalf("extension rollout changed durable handoff: %+v", got)
	}
	if len(s.effects) != n {
		t.Fatal("catalog change revived Bootstrap writes")
	}
	s.developmentGitops()
	if got := next.Status(context.Background()); got.State != Adopted {
		t.Fatalf("converged extension did not remain adopted: %+v", got)
	}
	if len(s.effects) != n {
		t.Fatal("observation mutated cluster")
	}
}
