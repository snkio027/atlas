package platform

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func candidate(t *testing.T) *Project {
	t.Helper()
	p, e := Load(testRoot(t), Tools{"helm", "kubectl", "yq"})
	if e != nil {
		t.Fatal(e)
	}
	return p
}

// unsealedCandidate models first-time onboarding with no active capabilities or
// instance credentials, independent of the source template's enabled selection.
func unsealedCandidate(t *testing.T) *Project {
	t.Helper()
	p := candidate(t)
	source := p.Root
	p.Root = t.TempDir()
	paths := []string{capabilityDir, scopeRegistryPath, platformApplications}
	for _, path := range p.Config.Components {
		paths = append(paths, path)
	}
	for _, c := range p.Capabilities.Catalog.Components {
		paths = append(paths, c.Path)
	}
	for _, dir := range paths {
		err := filepath.WalkDir(filepath.Join(source, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			b, err := readInput(path)
			if err != nil {
				return err
			}
			dest := filepath.Join(p.Root, rel)
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			return os.WriteFile(dest, b, 0600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(p.Root, capabilityDir, "resources/platform-credentials.yaml"), []byte(`{"apiVersion":"v1","kind":"List","items":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	p.Capabilities.Active = nil
	p.Capabilities.Enabled.Capabilities = nil
	core, e := p.CapabilityActivation(nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Write(core); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestCapabilityClosureAndReadinessOrder(t *testing.T) {
	p := candidate(t)
	names, e := p.ResolveCapabilities([]string{"storage-monitoring"})
	if e != nil {
		t.Fatal(e)
	}
	if len(names) != 11 || names[0] != "observability-foundation" || names[len(names)-1] != "storage-monitoring" {
		t.Fatal(names)
	}
	if _, e = p.ResolveCapabilities([]string{"unknown"}); e == nil {
		t.Fatal("unknown accepted")
	}
	x := p.Capabilities.Catalog.Components["monitoring-crds"]
	x.DependsOn = []string{"monitoring"}
	p.Capabilities.Catalog.Components["monitoring-crds"] = x
	if _, e = p.ResolveCapabilities([]string{"monitoring"}); e == nil {
		t.Fatal("cycle accepted")
	}
}
func TestCandidatePlanReportsMissingCredentials(t *testing.T) {
	p := unsealedCandidate(t)
	plan, e := p.CapabilityPlan([]string{"storage-monitoring"})
	if e != nil {
		t.Fatal(e)
	}
	if plan["readyToEnable"] != false || len(plan["missingSealedSecrets"].([]string)) != 5 {
		t.Fatal(plan)
	}
	plan, e = p.CapabilityPlan([]string{"secrets-controller"})
	if e != nil || plan["readyToEnable"] != true {
		t.Fatal(plan, e)
	}
}
func TestCapabilityProjectionPreservesCoreAndTenant(t *testing.T) {
	p := candidate(t)
	names, e := p.ResolveCapabilities([]string{"storage-monitoring"})
	if e != nil {
		t.Fatal(e)
	}
	files, e := p.CapabilityActivation(names)
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 2 {
		t.Fatal("unexpected authority edits", len(files))
	}
	apps, e := decodeObjects(files[platformApplications])
	if e != nil {
		t.Fatal(e)
	}
	core := resourceFile(t, capabilityDir+"/core-applications.json")
	if !bytes.Equal(encoded(apps[:len(core)]), encoded(core)) {
		t.Fatal("core Applications changed")
	}
	projects, e := decodeObjects(files[platformProjects])
	if e != nil {
		t.Fatal(e)
	}
	original := resourceFile(t, capabilityDir+"/core-projects.json")
	if !bytes.Equal(encoded(projects[1:]), encoded(original[1:])) {
		t.Fatal("tenant authority changed")
	}
	if !allowed(field(projects[0], "spec", "namespaceResourceWhitelist"), "monitoring.coreos.com", "Prometheus") {
		t.Fatal("missing exact monitoring capability")
	}
	if allowed(field(projects[0], "spec", "namespaceResourceWhitelist"), "", "Secret") {
		t.Fatal("plaintext Secret authority added")
	}
}
func TestUnrenderedOrForgedCatalogCannotActivate(t *testing.T) {
	p := unsealedCandidate(t)
	p.Capabilities.Active = []string{"monitoring"}
	if e := p.ValidateCapabilityActivation(); e == nil {
		t.Fatal("missing credentials accepted")
	}
	p.Capabilities.Active = []string{"secrets-foundation"}
	if e := p.ValidateCapabilityActivation(); e == nil {
		t.Fatal("enabled catalog without rendered control accepted")
	}
}
func TestPlaintextCredentialTemplateRejected(t *testing.T) {
	p := candidate(t)
	p.Root = t.TempDir()
	dir := filepath.Join(p.Root, capabilityDir, "resources")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	input := `{"kind":"SealedSecret","apiVersion":"bitnami.com/v1alpha1","metadata":{"name":"grafana-admin","namespace":"monitoring"},"spec":{"template":{"metadata":{"name":"grafana-admin","namespace":"monitoring"},"stringData":{"admin-password":"test-only"}}}}`
	if e := os.WriteFile(filepath.Join(dir, "platform-credentials.json"), []byte(input), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := p.missingCapabilitySecrets([]string{"monitoring"}); e == nil {
		t.Fatal("plaintext accepted")
	}
}

func TestCatalogJSONRejectsDuplicateKeys(t *testing.T) {
	for _, s := range []string{`{"schema":1,"schema":2}`, `{"components":{"x":{"path":"a","path":"b"}}}`, `{} {}`} {
		if uniqueJSONKeys([]byte(s)) == nil {
			t.Fatal("accepted ambiguous JSON", s)
		}
	}
	if e := uniqueJSONKeys([]byte(`{"x":[{"a":1},{"a":2}]}`)); e != nil {
		t.Fatal(e)
	}
}
func TestCandidateInstantiationSnapshotRemainsFrozen(t *testing.T) {
	b, e := readInput(filepath.Join(testRoot(t), inputDir, "bootstrap/baseline-v3.json"))
	if e != nil {
		t.Fatal(e)
	}
	if hash(b) != "6971d4560e39e6148f6155f7f4263181e9b8df62c1c8706ba6a9f00761ae7ab9" {
		t.Fatal("historical instantiation snapshot was rewritten")
	}
}

func TestCatalogCannotInjectHelmFlags(t *testing.T) {
	source := testRoot(t)
	root := t.TempDir()
	for _, path := range []string{inputDir + "/config.json", inputDir + "/versions.lock.json", capabilityDir + "/catalog.json", capabilityDir + "/enabled.json", capabilityDir + "/versions.lock.json"} {
		b, e := readInput(filepath.Join(source, path))
		if e != nil {
			t.Fatal(e)
		}
		dest := filepath.Join(root, path)
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	path := filepath.Join(root, capabilityDir, "catalog.json")
	var c CapabilityCatalog
	if e := readJSON(path, &c); e != nil {
		t.Fatal(e)
	}
	c.Jobs[0].Release = "--post-renderer=/tmp/untrusted"
	b, _ := json.Marshal(c)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(root, Tools{"helm", "kubectl", "yq"}); e == nil {
		t.Fatal("Helm flag injection accepted")
	}
}
