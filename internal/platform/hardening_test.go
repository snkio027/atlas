package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func registryFixture(t *testing.T) ScopeRegistry {
	t.Helper()
	var reg ScopeRegistry
	if e := readJSON(filepath.Join(testRoot(t), scopeRegistryPath), &reg); e != nil {
		t.Fatal(e)
	}
	return reg
}
func customDefinition(scope string) Object {
	return Object{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": Object{"name": "widgets.example.test"}, "spec": Object{
		"group": "example.test", "scope": scope, "names": Object{"kind": "Widget", "plural": "widgets"},
		"versions": []any{
			Object{"name": "v1", "served": true, "storage": true, "schema": Object{"openAPIV3Schema": Object{"type": "object", "properties": Object{"spec": Object{"type": "object", "properties": Object{"value": Object{"type": "string"}}}}}}},
			Object{"name": "v1beta1", "served": false, "storage": false},
		},
	}}
}
func TestScopeRegistryAndCRDVersionFences(t *testing.T) {
	reg := registryFixture(t)
	m, e := newResourceModel(reg, "1.37.0", []Object{customDefinition("Cluster")})
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		api, kind string
		want      ResourceScope
	}{
		{"apiregistration.k8s.io/v1", "APIService", ClusterScope},
		{"storage.k8s.io/v1", "StorageClass", ClusterScope},
		{"node.k8s.io/v1", "RuntimeClass", ClusterScope},
		{"scheduling.k8s.io/v1", "PriorityClass", ClusterScope},
		{"apps/v1", "Deployment", NamespacedScope},
		{"v1", "Service", NamespacedScope},
		{"example.test/v1", "Widget", ClusterScope},
	} {
		r, e := m.Resolve(Object{"apiVersion": tc.api, "kind": tc.kind})
		if e != nil || r.Scope != tc.want {
			t.Fatalf("%+v: %+v / %v", tc, r, e)
		}
	}
	for _, o := range []Object{
		{"apiVersion": "example.test/v1beta1", "kind": "Widget"},
		{"apiVersion": "example.test/v2", "kind": "Widget"},
		{"apiVersion": "apiregistration.k8s.io/v999", "kind": "APIService"},
		{"apiVersion": "apps/v1", "kind": "Typo"},
	} {
		if _, e = m.Resolve(o); e == nil {
			t.Fatalf("unknown GVK accepted: %v", o)
		}
	}
	for _, scope := range []string{"Cluster", "Namespaced"} {
		m, e = newResourceModel(reg, "1.37.0", []Object{customDefinition(scope)})
		if e != nil {
			t.Fatal(e)
		}
		o := Object{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": Object{"name": "one"}, "spec": Object{"value": "ok"}}
		if e = m.Validate(o, "tenant"); e != nil {
			t.Fatal(e)
		}
		mapping(o["spec"])["typo"] = true
		if e = m.Validate(o, "tenant"); e == nil {
			t.Fatal("schema typo accepted")
		}
		delete(mapping(o["spec"]), "typo")
		metadata(o)["namespace"] = "tenant"
		if e = m.Validate(o, "tenant"); (e == nil) != (scope == "Namespaced") {
			t.Fatal("namespace classification differs from CRD scope", e)
		}
	}
	for _, mutate := range []func(*ScopeRegistry){
		func(r *ScopeRegistry) { r.Kubernetes = "1.35.1" },
		func(r *ScopeRegistry) { r.Resources = append(r.Resources, r.Resources[0]) },
		func(r *ScopeRegistry) { r.Resources[0].Scope = "Unknown" },
	} {
		r := registryFixture(t)
		mutate(&r)
		if _, e = newResourceModel(r, "1.37.0", nil); e == nil {
			t.Fatal("invalid registry accepted")
		}
	}
	for _, objects := range [][]Object{{customDefinition("Unknown")}, {customDefinition("Cluster"), customDefinition("Namespaced")}} {
		if _, e = newResourceModel(reg, "1.37.0", objects); e == nil {
			t.Fatal("invalid/conflicting CRD accepted")
		}
	}
}

func treeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	e := filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := readInput(path)
		if e != nil {
			return e
		}
		out[path] = hash(b)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestRetirementFailsBeforeToolsOrWrites(t *testing.T) {
	p := unsealedCandidate(t)
	p.Capabilities.Active = []string{"secrets-foundation", "secrets-crds", "secrets-controller"}
	before := treeHashes(t, p.Root)
	p.Tools = Tools{"must-not-run", "must-not-run", "must-not-run"}
	e := p.SelectCapabilities(context.Background(), []string{"secrets-foundation"})
	if e == nil || !strings.Contains(e.Error(), "enable-only") {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, treeHashes(t, p.Root)) {
		t.Fatal("retirement attempt changed files")
	}
	full, e := p.ResolveCapabilities([]string{"storage-monitoring"})
	if e != nil {
		t.Fatal(e)
	}
	if e = p.ValidateCapabilitySelection(full); e != nil {
		t.Fatal("dependency closure did not preserve existing selection", e)
	}
}

func TestManualSelectionEditAndLegacyOwnerCannotHideRemoval(t *testing.T) {
	p := unsealedCandidate(t)
	for _, names := range [][]string{{"monitoring"}, {"capability-foundation"}} {
		core := resourceFile(t, capabilityDir+"/core-applications.json")
		for _, name := range names {
			core = append(core, Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": Object{"name": name, "namespace": "platform-argocd"}})
		}
		b, _ := json.Marshal(Object{"apiVersion": "v1", "kind": "List", "items": core})
		if e := p.Write(map[string][]byte{platformApplications: b}); e != nil {
			t.Fatal(e)
		}
		// Even if enabled.json was edited to drop everything, the previous
		// rendered owner must not disappear during a normal render/select.
		p.Capabilities.Active = nil
		p.Tools = Tools{"must-not-run", "must-not-run", "must-not-run"}
		if _, e := p.Render(context.Background()); e == nil || !strings.Contains(e.Error(), names[0]) {
			t.Fatal(e)
		}
		if e := p.ValidateCapabilitySelection(nil); e == nil || !strings.Contains(e.Error(), names[0]) {
			t.Fatal(e)
		}
	}
	if e := os.Remove(filepath.Join(p.Root, platformApplications)); e != nil {
		t.Fatal(e)
	}
	if e := p.ValidateCapabilitySelection(nil); e == nil {
		t.Fatal("unavailable previous projection treated as empty")
	}
	for _, invalid := range []string{"", `{"apiVersion":"v1","kind":"List","items":[]}`} {
		if e := p.Write(map[string][]byte{platformApplications: []byte(invalid)}); e != nil {
			t.Fatal(e)
		}
		if e := p.ValidateCapabilitySelection(nil); e == nil {
			t.Fatal("empty/truncated projection treated as an empty baseline")
		}
	}
}

func TestPlanReportsRetirementAndSharedDomainAuthority(t *testing.T) {
	p := candidate(t)
	// Explicit full-selection fixture, independent of the deployment stage.
	var err error
	p.Capabilities.Active, err = p.ResolveCapabilities([]string{"storage-monitoring"})
	if err != nil {
		t.Fatal(err)
	}
	plan, e := p.CapabilityPlan([]string{"secrets-controller"})
	if e != nil {
		t.Fatal(e)
	}
	if plan["readyToEnable"] != false || len(plan["removedCapabilities"].([]string)) == 0 || plan["retirementSupported"] != false {
		t.Fatal(plan)
	}
	full, e := p.CapabilityPlan([]string{"storage-monitoring"})
	if e != nil {
		t.Fatal(e)
	}
	// Source templates have no installation ciphertext. A complete selection
	// remains blocked until that instance supplies all five required keys.
	if full["readyToEnable"] != false || len(full["missingSealedSecrets"].([]string)) != 5 || len(full["removedCapabilities"].([]string)) != 0 {
		t.Fatal(full)
	}
	for _, step := range full["resolved"].([]any) {
		s := mapping(step)
		if s["appProject"] != "platform-project" || s["permissionDomain"] == "" {
			t.Fatal(step)
		}
	}
	if !strings.Contains(str(full["permissionBoundary"]), "not isolation") {
		t.Fatal("domain isolation overstated")
	}
}

func TestAPIServiceAndCustomClusterScopeProjectIntoClusterAuthority(t *testing.T) {
	p := unsealedCandidate(t)
	p.Capabilities.Catalog.Components["scope-probe"] = Capability{PermissionDomain: "security", Path: "gitops/platform/scope-probe/overlays/development", Namespace: "sealed-secrets", Wave: 0}
	objects := []Object{customDefinition("Cluster"), {"apiVersion": "example.test/v1", "kind": "Widget", "metadata": Object{"name": "probe"}, "spec": Object{"value": "ok"}},
		{"apiVersion": "apiregistration.k8s.io/v1", "kind": "APIService", "metadata": Object{"name": "v1beta1.metrics.k8s.io"}}}
	path := p.Capabilities.Catalog.Components["scope-probe"].Path
	b, _ := json.Marshal(Object{"apiVersion": "v1", "kind": "List", "items": objects})
	if e := p.Write(map[string][]byte{path + "/resources.json": b, path + "/kustomization.yaml": []byte(`{"apiVersion":"kustomize.config.k8s.io/v1beta1","kind":"Kustomization","resources":["resources.json"]}`)}); e != nil {
		t.Fatal(e)
	}
	files, e := p.CapabilityActivation([]string{"scope-probe"})
	if e != nil {
		t.Fatal(e)
	}
	projects, e := decodeObjects(files[platformProjects])
	if e != nil {
		t.Fatal(e)
	}
	apps, e := decodeObjects(files[platformApplications])
	if e != nil {
		t.Fatal(e)
	}
	m, e := p.capabilityResourceModel([]string{"scope-probe"})
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range objects[1:] {
		if !allowed(field(projects[0], "spec", "clusterResourceWhitelist"), group(o), str(o["kind"])) || allowed(field(projects[0], "spec", "namespaceResourceWhitelist"), group(o), str(o["kind"])) {
			t.Fatal("scope projection mismatch", o)
		}
		if e = permitted(projects[0], apps[len(apps)-1], o, m); e != nil {
			t.Fatal(e)
		}
		metadata(o)["namespace"] = "sealed-secrets"
		if e = permitted(projects[0], apps[len(apps)-1], o, m); e == nil {
			t.Fatal("cluster resource with namespace accepted")
		}
		delete(metadata(o), "namespace")
	}
	objects[2]["apiVersion"] = "apiregistration.k8s.io/v999"
	b, _ = json.Marshal(Object{"apiVersion": "v1", "kind": "List", "items": objects})
	if e = p.Write(map[string][]byte{path + "/resources.json": b}); e != nil {
		t.Fatal(e)
	}
	if _, e = p.CapabilityActivation([]string{"scope-probe"}); e == nil || !strings.Contains(e.Error(), "unknown resource GVK") {
		t.Fatal(e)
	}
}

func TestFoundationsAreIndependentAndHaveUniqueOwners(t *testing.T) {
	p := candidate(t)
	names, e := p.ResolveCapabilities([]string{"secrets-controller"})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(names, []string{"secrets-foundation", "secrets-crds", "secrets-controller"}) {
		t.Fatal(names)
	}
	for _, name := range names {
		objects := resourceFile(t, capabilityDir+"/resources/"+name+".json")
		for _, o := range objects {
			ns := str(metadata(o)["namespace"])
			if o["kind"] == "Namespace" {
				ns = str(metadata(o)["name"])
			}
			if ns != "" && ns != "platform-sealed-secrets" && ns != "project-platform-smoke" {
				t.Fatal("secrets-only pulls unrelated namespace objects", identity(o))
			}
		}
	}
	seen := map[string]string{}
	for _, name := range []string{"secrets-foundation", "observability-foundation", "storage-foundation"} {
		objects, e := p.capabilityObjects(name)
		if e != nil {
			t.Fatal(e)
		}
		for _, o := range objects {
			id := identity(o)
			if seen[id] != "" {
				t.Fatal("duplicate foundation owner", id)
			}
			if o["kind"] == "Role" || o["kind"] == "RoleBinding" {
				t.Fatal("foundation owns controller RBAC", id)
			}
			seen[id] = name
		}
	}
	if len(seen) != 13 {
		t.Fatal("expected exactly 13 domain foundation objects", len(seen))
	}
	files, e := p.CapabilityActivation(p.Capabilities.Active)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(files[platformApplications], []byte(`"name": "capability-foundation"`)) {
		t.Fatal("legacy owner remains active")
	}
}

// Exercise actual chart overrides and controller-owned namespace RBAC without touching
// a cluster. The full chart candidate remains offline-renderable when disabled.
func TestSealedSecretsNamespaceSelectionRealHelm(t *testing.T) {
	helm := os.Getenv("ATLAS_TEST_HELM")
	if helm == "" {
		t.Skip("set ATLAS_TEST_HELM for real offline renderer")
	}
	p := candidate(t)
	p.Tools = Tools{Helm: helm, Kubectl: filepath.Join(filepath.Dir(helm), "kubectl"), YQ: filepath.Join(filepath.Dir(helm), "yq")}
	for _, tc := range []struct {
		request []string
		watched string
		bound   int
	}{
		{[]string{"secrets-controller"}, "platform-sealed-secrets,project-platform-smoke", 0},
		{[]string{"secrets-controller", "observability-foundation"}, "platform-sealed-secrets,platform-monitoring,project-platform-smoke", 2},
		{[]string{"secrets-controller", "storage-foundation"}, "platform-sealed-secrets,project-platform-smoke,service-object-storage", 2},
		// The disabled controller has a complete offline candidate; foundations
		// must never receive its RBAC, even when only monitoring CRDs are active.
		{[]string{"monitoring-crds"}, "platform-sealed-secrets,platform-cnpg-operator,platform-flink-operator,platform-monitoring,platform-strimzi-operator,project-platform-smoke,service-flink-session,service-object-storage,service-postgresql,service-redis", 16},
		{[]string{"storage-monitoring"}, "platform-sealed-secrets,platform-monitoring,project-platform-smoke,service-object-storage", 4},
		{[]string{"secrets-controller", "postgresql-foundation", "redis-foundation"}, "platform-sealed-secrets,project-platform-smoke,service-postgresql,service-redis", 4},
	} {
		names, e := p.ResolveCapabilities(tc.request)
		if e != nil {
			t.Fatal(e)
		}
		p.Capabilities.Active = names
		files, e := p.RenderCapabilities(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		objects, e := DecodeJSONManifests(files[p.Capabilities.Catalog.Components["secrets-controller"].Path+"/rendered.yaml"])
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, o := range objects {
			if o["kind"] == "Deployment" {
				containers := slice(field(o, "spec", "template", "spec", "containers"))
				args := slice(field(mapping(containers[0]), "args"))
				for i, a := range args {
					if a == "--additional-namespaces" && i+1 < len(args) {
						found = args[i+1] == tc.watched
					}
				}
			}
		}
		if !found {
			t.Fatal("wrong watched namespace rollout", tc)
		}
		count := 0
		for _, o := range objects {
			ns := str(metadata(o)["namespace"])
			if o["kind"] == "Role" || o["kind"] == "RoleBinding" {
				if !strings.Contains(","+tc.watched+",", ","+ns+",") {
					t.Fatal("controller RBAC outside watched namespaces", identity(o))
				}
				if ns != "platform-sealed-secrets" && ns != "project-platform-smoke" {
					count++
				}
			}
		}
		for _, name := range []string{"observability-foundation", "storage-foundation", "postgresql-foundation", "redis-foundation"} {
			if _, exists := files[p.Capabilities.Catalog.Components[name].Path+"/rendered.yaml"]; exists {
				t.Fatal("controller chart emitted foundation resources")
			}
		}
		if reflect.DeepEqual(tc.request, []string{"monitoring-crds"}) {
			activation, e := p.CapabilityActivation(names)
			if e != nil {
				t.Fatal(e)
			}
			apps, e := decodeObjects(activation[platformApplications])
			if e != nil {
				t.Fatal(e)
			}
			for _, app := range apps {
				if metadata(app)["name"] == "secrets-controller" {
					t.Fatal("monitoring CRDs activate secrets controller")
				}
			}
		}
		if count != tc.bound {
			t.Fatal("wrong controller-owned namespace RBAC", count, tc.bound)
		}
	}
}

func TestDomainMappingsRejectUnknownAndNonCanonicalProjects(t *testing.T) {
	for _, tc := range []string{"missing", "unknown", "split-project", "empty-domain"} {
		t.Run(tc, func(t *testing.T) {
			p := unsealedCandidate(t)
			for _, path := range []string{inputDir + "/config.json", inputDir + "/versions.lock.json"} {
				b, e := readInput(filepath.Join(testRoot(t), path))
				if e != nil {
					t.Fatal(e)
				}
				if e = p.Write(map[string][]byte{path: b}); e != nil {
					t.Fatal(e)
				}
			}
			c := p.Capabilities.Catalog
			switch tc {
			case "missing":
				c.PermissionDomains = nil
			case "unknown":
				x := c.Components["monitoring"]
				x.PermissionDomain = "unknown"
				c.Components["monitoring"] = x
			case "split-project":
				c.PermissionDomains["security"] = "security-project"
			case "empty-domain":
				x := c.Components["monitoring"]
				x.PermissionDomain = ""
				c.Components["monitoring"] = x
			}
			b, _ := json.Marshal(c)
			if e := p.Write(map[string][]byte{capabilityDir + "/catalog.json": b}); e != nil {
				t.Fatal(e)
			}
			_, e := Load(p.Root, Tools{"not-run", "not-run", "not-run"})
			if e == nil || !(strings.Contains(e.Error(), "permission domain") || strings.Contains(e.Error(), "invalid Tier-1 capability")) {
				t.Fatal(e)
			}
		})
	}
}

func TestSelectAddAndRepeatRealHelm(t *testing.T) {
	helm := os.Getenv("ATLAS_TEST_HELM")
	if helm == "" {
		t.Skip("set ATLAS_TEST_HELM for real offline selection")
	}
	p := unsealedCandidate(t)
	p.Tools = Tools{Helm: helm, Kubectl: filepath.Join(filepath.Dir(helm), "kubectl"), YQ: filepath.Join(filepath.Dir(helm), "yq")}
	for path := range p.Capabilities.Lock.Artifacts {
		b, e := readInput(filepath.Join(testRoot(t), path))
		if e != nil {
			t.Fatal(e)
		}
		if e = p.Write(map[string][]byte{path: b}); e != nil {
			t.Fatal(e)
		}
	}
	if e := p.SelectCapabilities(context.Background(), []string{"secrets-controller"}); e != nil {
		t.Fatal(e)
	}
	// Only envelope structure matters to selection. Use non-decryptable fixture
	// bytes rather than an instance's public ciphertext. Real sealing is covered
	// independently by TestOfflineCredentialPreparationIsPrivateScopedAndRepeatable.
	cipher := make([]byte, 300)
	cipher[0] = 1 // 256-byte wrapped-key envelope, followed by synthetic payload.
	items := []Object{}
	for _, target := range []struct {
		namespace, name string
		keys            []string
	}{
		{"platform-monitoring", "grafana-admin", []string{"admin-user", "admin-password"}},
		{"service-object-storage", "seaweedfs-auth", []string{"seaweedfs_s3_config"}},
		{"project-platform-smoke", "s3-client", []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}},
	} {
		data := Object{}
		for _, key := range target.keys {
			data[key] = base64.StdEncoding.EncodeToString(cipher)
		}
		m := Object{"namespace": target.namespace, "name": target.name}
		items = append(items, Object{"apiVersion": "bitnami.com/v1alpha1", "kind": "SealedSecret", "metadata": m, "spec": Object{"encryptedData": data, "template": Object{"metadata": m, "type": "Opaque"}}})
	}
	path := capabilityDir + "/resources/platform-credentials.json"
	b, e := json.Marshal(Object{"apiVersion": "v1", "kind": "List", "items": items})
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Write(map[string][]byte{path: b}); e != nil {
		t.Fatal(e)
	}
	if e = p.SelectCapabilities(context.Background(), []string{"storage-monitoring"}); e != nil {
		t.Fatal(e)
	}
	before := treeHashes(t, p.Root)
	if e = p.SelectCapabilities(context.Background(), []string{"storage-monitoring"}); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, treeHashes(t, p.Root)) {
		t.Fatal("repeated selection changed output bytes")
	}
	if e = p.SelectCapabilities(context.Background(), []string{"secrets-controller"}); e == nil || !strings.Contains(e.Error(), "enable-only") {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, treeHashes(t, p.Root)) {
		t.Fatal("rejected retirement changed bytes")
	}
}
