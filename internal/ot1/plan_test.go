package ot1

import (
	"context"
	"encoding/base64"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCiphertextFixture(t *testing.T, repo string) {
	t.Helper()
	// Intentionally non-decryptable syntax fixture, never runtime credentials.
	// This only exercises offline projection. Live baseline ADOPTED is a separate gate.
	ciphertext := make([]byte, 2+256+32)
	ciphertext[0] = 1
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	items := []any{}
	for _, s := range []struct {
		ns, name string
		keys     []string
	}{{"atlas-monitoring", "grafana-admin", []string{"admin-user", "admin-password"}}, {"atlas-storage", "seaweedfs-auth", []string{"seaweedfs_s3_config"}}, {"workload-web", "s3-client", []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}}} {
		data := observation.Object{}
		for _, key := range s.keys {
			data[key] = encoded
		}
		items = append(items, observation.Object{"apiVersion": "bitnami.com/v1alpha1", "kind": "SealedSecret", "metadata": observation.Object{"name": s.name, "namespace": s.ns}, "spec": observation.Object{"encryptedData": data, "template": observation.Object{"metadata": observation.Object{"name": s.name, "namespace": s.ns}, "type": "Opaque"}}})
	}
	value := observation.Object{"apiVersion": "v1", "kind": "List", "items": items}
	for _, path := range []string{CredentialInput, CredentialOutput} {
		if e := jsonFile(repo, path, value); e != nil {
			t.Fatal(e)
		}
	}
}
func frozenRenderTools(t *testing.T) platform.Tools {
	t.Helper()
	dir := os.Getenv("ATLAS_TEST_OT1_TOOL_DIR")
	if dir == "" {
		if os.Getenv("ATLAS_TEST_HELM") != "" {
			t.Fatal("OT-1 requires separate historical tools: set ATLAS_TEST_OT1_TOOL_DIR")
		}
		t.Skip("set ATLAS_TEST_OT1_TOOL_DIR for frozen offline rehearsal rendering")
	}
	return platform.Tools{Helm: filepath.Join(dir, "helm"), Kubectl: filepath.Join(dir, "kubectl"), YQ: filepath.Join(dir, "yq")}
}

func fullPlanFixture(t *testing.T) (Plan, string, string, platform.Tools) {
	t.Helper()
	tools := frozenRenderTools(t)
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(base, 0700); e != nil {
		t.Fatal(e)
	}
	repo := filepath.Join(base, "baseline")
	if _, e = PrepareRepository(context.Background(), root, repo, tools); e != nil {
		t.Fatal(e)
	}
	fakeCiphertextFixture(t, repo)
	p, e := platform.Load(repo, tools)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.SelectCapabilities(context.Background(), []string{"monitoring", "object-storage", "storage-monitoring"}); e != nil {
		t.Fatal(e)
	}
	p, e = platform.Load(repo, tools)
	if e != nil {
		t.Fatal(e)
	}
	files, e := p.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Write(files); e != nil {
		t.Fatal(e)
	}
	if _, e = commitLocal(context.Background(), repo, "Synthetic full-capability baseline for offline tests only"); e != nil {
		t.Fatal(e)
	}
	target := observation.Target{Cluster: developmentprofile.OT1Cluster, Context: "kind-" + developmentprofile.OT1Cluster, ClusterUID: "synthetic-cluster-uid", KubeconfigSHA256: strings.Repeat("a", 64)}
	impl := observation.Implementation{Revision: strings.Repeat("b", 40), BinarySHA256: strings.Repeat("c", 64)}
	clone := filepath.Join(base, "plan")
	plan, e := CompilePlan(context.Background(), root, repo, clone, target, impl, tools)
	if e != nil {
		t.Fatal(e)
	}
	return plan, clone, root, tools
}
func TestPlanFullGraphAndFrozenAuthority(t *testing.T) {
	plan, repo, root, tools := fullPlanFixture(t)
	if len(plan.Phases) != 29 || len(plan.Revisions) != 7 {
		t.Fatal("incomplete plan")
	}
	scope, stages, e := LoadContracts(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, phase := range plan.Phases {
		if !phase.Stage.AtlasGate {
			continue
		}
		count := 24
		if phase.Stage.Name == "FORWARD_VERIFIED" {
			count = 26
		}
		if len(phase.Applications) != count {
			t.Fatalf("%s expected %d Apps, got %d", phase.Stage.Name, count, len(phase.Applications))
		}
		if _, e = git(context.Background(), repo, "checkout", "--detach", phase.Revision); e != nil {
			t.Fatal(e)
		}
		p, e := platform.Load(repo, tools)
		if e != nil {
			t.Fatal(e)
		}
		if e = p.ValidateCapabilityActivation(); e != nil {
			t.Fatal(e)
		}
	}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Target.Cluster = "atlas-refactor-test-dev02" },
		func(p *Plan) { p.Branch = developmentprofile.DevelopmentRevision },
		func(p *Plan) { p.MaxStageSeconds = 301 },
		func(p *Plan) { p.Phases = p.Phases[1:] },
		func(p *Plan) { p.Scope.Objects = p.Scope.Objects[:12] },
		func(p *Plan) { p.Implementation.Dirty = true },
		func(p *Plan) { p.Phases[2].Stage.Applications["secrets-foundation"] = "window" },
	} {
		var bad Plan
		if e = observation.Decode(observation.Bytes(plan), &bad, true); e != nil {
			t.Fatal(e)
		}
		mutate(&bad)
		if ValidatePlan(bad, stages, scope) == nil {
			t.Fatal("expanded plan accepted")
		}
	}
	if e = VerifyRepositoryPlan(context.Background(), repo, plan); e != nil {
		t.Fatal(e)
	}
}

func TestParentProjectionRejectsHiddenScopeExpansion(t *testing.T) {
	old, _ := Application(Source, true)
	observation.Map(observation.At(old, "spec", "syncPolicy"))["automated"] = observation.Object{"prune": true, "selfHeal": true}
	other := observation.Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": observation.Object{"name": "secrets-controller", "namespace": "argocd"}, "spec": observation.Object{"project": "platform-project", "source": observation.Object{"repoURL": developmentprofile.Repository}}}
	baseline := []observation.Object{old, other}
	stage := Stage{AtlasGate: false, Applications: map[string]string{}}
	if e := checkApplicationProjection(baseline, []observation.Object{other}, stage); e != nil {
		t.Fatal(e)
	}
	changed := observation.Clone(other)
	observation.Map(changed["spec"])["project"] = "atlas-bootstrap"
	if e := checkApplicationProjection(baseline, []observation.Object{changed}, stage); e == nil {
		t.Fatal("Tier-0 project escalation hidden in allowed file")
	}
	changed = observation.Clone(other)
	observation.Map(changed["metadata"])["annotations"] = observation.Object{"argocd.argoproj.io/sync-options": "Prune=true"}
	if e := checkApplicationProjection(baseline, []observation.Object{changed}, stage); e == nil {
		t.Fatal("metadata mutation hidden in allowed file")
	}
	if e := checkApplicationProjection(baseline, []observation.Object{other, old}, stage); e == nil {
		t.Fatal("source owner still in detached parent")
	}
}
