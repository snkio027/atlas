package workloadrun

import (
	"atlas-refactor/internal/observation"
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/workload"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Use real compiled Git trees and the pinned scope model, but no live cluster,
// credential, subprocess read, or public publication. Receipts and UIDs are synthetic.
func compiledRollout(t *testing.T) (workload.CompileContext, map[string]workload.Result) {
	t.Helper()
	product, err := platform.Load("../..", platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := product.BundleFiles()
	if err != nil {
		t.Fatal(err)
	}
	base := Files{}
	for name, b := range bundle {
		if strings.HasPrefix(name, "gitops/") {
			base[name] = b
		}
	}
	base["gitops/platform/management/argocd-self/overlays/development/signal.json"] = workload.JSON(Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"name": "atlas-refactor-adoption-signal", "namespace": "platform-argocd"}})
	model, err := product.ObservationResourceModel()
	if err != nil {
		t.Fatal(err)
	}
	c := workload.CompileContext{Base: base, ResourceModel: model, Repository: "https://github.com/snkio027/atlas-refactor.git", Branch: "codex/development-platform", ProductSHA256: strings.Repeat("1", 64), CompilerSHA256: strings.Repeat("2", 64), InstallID: strings.Repeat("3", 32), CertificateSHA256: strings.Repeat("4", 64), HTTPSPort: 8443}
	intent, err := workload.ReadIntent("../../examples/s2")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := workload.Resolve(intent, true)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]workload.Result{}
	for _, phase := range []string{"permissions", "project", "infrastructure"} {
		result, err := workload.Compile(c, resolved, phase, nil)
		if err != nil {
			t.Fatal(err)
		}
		results[phase] = result
	}
	return c, results
}
func compiledGate(t *testing.T, r workload.Result, receipt Publication) Observation {
	t.Helper()
	report := Observation{Schema: 1, Phase: r.Inventory.Phase, ClusterUID: "fixture-cluster", Revision: receipt.Commit, Project: "VERIFIED", Workload: "UNKNOWN", Binding: "UNKNOWN", Runtime: "UNPROVEN", Gate: &GateDecision{State: gateReady}, Applications: map[string]string{}, Resources: map[string]string{}, UID: map[string]string{}}
	objects, err := desiredObjects(r.Files, r.Inventory.Resources)
	if err != nil {
		t.Fatal(err)
	}
	for _, owned := range r.Inventory.Resources {
		o := objects[owned.Identity]
		if o["kind"] != "Application" {
			continue
		}
		name := str(at(o, "metadata", "name"))
		_, live := gateApp(name)
		live["spec"] = o["spec"]
		mapping(at(live, "status", "sync"))["revision"] = receipt.Commit
		fact := observation.ClassifyApplication(observation.ExpectedApplication{Namespace: "platform-argocd", Name: name, Spec: mapping(o["spec"]), Revision: receipt.Commit}, live, nil)
		report.ApplicationFacts = append(report.ApplicationFacts, fact)
		report.Applications[name] = receipt.Commit
		report.UID[owned.Identity] = fact.UID
	}
	return report
}
func rolloutFixture(t *testing.T, c workload.CompileContext, rs map[string]workload.Result, current string) (*Workflow, Plan, Publication) {
	t.Helper()
	w, p := sequenceFixture(t)
	w.Context = c
	w.Install.Record.ClusterUID = "fixture-cluster"
	p.ClusterUID = w.Install.Record.ClusterUID
	p.CredentialTargets = []string{}
	for phase, r := range rs {
		p.PhaseSHA256[phase] = platform.BundleDigest(r.Files)
	}
	if err := w.SavePlan(p); err != nil {
		t.Fatal(err)
	}
	w.trees = map[string]Files{p.Parent: c.Base}
	if err := save(filepath.Join(filepath.Dir(w.publicationPath(p, current)), "baseline-uids.json"), []byte("{}"), true); err != nil {
		t.Fatal(err)
	}
	parent := p.Parent
	var receipt Publication
	for _, phase := range p.Phases {
		receipt = Publication{1, workload.Digest(workload.JSON(p)), phase, parent, workload.Digest([]byte(phase))[:40], p.PhaseSHA256[phase]}
		if err := save(w.publicationPath(p, phase), workload.JSON(receipt), true); err != nil {
			t.Fatal(err)
		}
		w.trees[receipt.Commit] = rs[phase].Files
		if phase == current {
			break
		}
		if err := w.retainGate(p, compiledGate(t, rs[phase], receipt)); err != nil {
			t.Fatal(err)
		}
		parent = receipt.Commit
	}
	return w, p, receipt
}
func TestRolloutContractUsesIntroductionAndPriorGateUIDs(t *testing.T) {
	c, rs := compiledRollout(t)
	for _, phase := range []string{"project", "infrastructure"} {
		w, _, receipt := rolloutFixture(t, c, rs, phase)
		s, err := w.rolloutContract(context.Background(), rs[phase], receipt.Commit)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		for name := range s.owners {
			if strings.HasPrefix(name, "s2-p-") {
				id = appIdentity(name)
			}
		}
		if id == "" {
			t.Fatal("Project app missing")
		}
		if phase == "project" && (s.prior[id] != nil || s.uid[id] != "") {
			t.Fatal("new app has predecessor")
		}
		if phase == "infrastructure" && (s.prior[id] == nil || s.uid[id] == "") {
			t.Fatal("prior Project identity lost")
		}
		if phase == "infrastructure" {
			if err = s.pin(id, "replacement"); err == nil {
				t.Fatal("replacement after successful prior Gate accepted")
			}
		}
	}
}
func TestRolloutContractClosesTransitionsAfterGateStopOrFinal(t *testing.T) {
	c, rs := compiledRollout(t)
	for _, marker := range []string{"project-gate.json", "terminal.json", "final.json"} {
		t.Run(marker, func(t *testing.T) {
			w, p, receipt := rolloutFixture(t, c, rs, "project")
			data := []byte("{}")
			if marker == "project-gate.json" {
				data = workload.JSON(compiledGate(t, rs["project"], receipt))
			}
			if err := save(filepath.Join(filepath.Dir(w.publicationPath(p, "project")), marker), data, true); err != nil {
				t.Fatal(err)
			}
			s, err := w.rolloutContract(context.Background(), rs["project"], receipt.Commit)
			if err != nil || s.active {
				t.Fatal("closed phase allowed transitions", err)
			}
		})
	}
}
func TestRolloutContractRejectsCurrentReceiptDrift(t *testing.T) {
	c, rs := compiledRollout(t)
	for _, field := range []string{"schema", "plan", "phase", "parent", "commit", "tree", "absent"} {
		t.Run(field, func(t *testing.T) {
			w, p, receipt := rolloutFixture(t, c, rs, "project")
			revision := receipt.Commit
			switch field {
			case "schema":
				receipt.Schema = 0
			case "plan":
				receipt.PlanSHA256 = "other"
			case "phase":
				receipt.Phase = "consumer"
			case "parent":
				receipt.Parent = p.Parent
			case "commit":
				receipt.Commit = strings.Repeat("f", 40)
			case "tree":
				receipt.TreeSHA256 = "other"
			}
			path := w.publicationPath(p, "project")
			if field == "absent" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err := save(path, workload.JSON(receipt), false); err != nil {
				t.Fatal(err)
			}
			if _, err := w.rolloutContract(context.Background(), rs["project"], revision); err == nil {
				t.Fatal("unbound publication accepted")
			}
		})
	}
}
func TestRolloutContractRequiresCompletePredecessorEvidence(t *testing.T) {
	c, rs := compiledRollout(t)
	for _, field := range []string{"absent", "phase", "revision", "cluster", "waiting", "missing uid", "changed uid", "missing resource uid"} {
		t.Run(field, func(t *testing.T) {
			w, p, receipt := rolloutFixture(t, c, rs, "infrastructure")
			prior, err := w.precedingPublication(p, "infrastructure")
			if err != nil {
				t.Fatal(err)
			}
			gate := compiledGate(t, rs["project"], *prior)
			switch field {
			case "phase":
				gate.Phase = "permissions"
			case "revision":
				gate.Revision = p.Parent
			case "cluster":
				gate.ClusterUID = "other"
			case "waiting":
				gate.Gate.State = gateWaiting
			case "missing uid":
				gate.UID = map[string]string{}
			case "missing resource uid":
				gate.Resources["v1/ConfigMap/demo/new"] = strings.Repeat("b", 64)
			case "changed uid":
				// One shared Application must not acquire a different identity between
				// successful permissions and project observations.
				f := &gate.ApplicationFacts[0]
				f.UID = "replacement"
				gate.UID[appIdentity(f.Ref.Name)] = f.UID
			}
			path := filepath.Join(filepath.Dir(w.publicationPath(p, "project")), "project-gate.json")
			if field == "absent" {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err = save(path, workload.JSON(gate), false); err != nil {
				t.Fatal(err)
			}
			if _, err = w.rolloutContract(context.Background(), rs["infrastructure"], receipt.Commit); err == nil {
				t.Fatal("broken phase evidence accepted")
			}
		})
	}
}

func TestObservationContractFailureArchivesCurrentPhase(t *testing.T) {
	w, p := sequenceFixture(t)
	r := workload.Result{Inventory: workload.Inventory{Phase: "project"}}
	// Missing plan/receipt fails before any external read. A stale successful
	// report must not be mistaken for the current phase's STOP evidence.
	path := filepath.Join(w.Config.StateDirectory, "latest-observation.json")
	if err := save(path, []byte(`{"phase":"permissions","gate":{"state":"Ready"}}`), false); err != nil {
		t.Fatal(err)
	}
	report, err := w.observeWait(context.Background(), r, p.Parent, 0)
	if err == nil || report.Gate.State != gateRejected {
		t.Fatal(report, err)
	}
	b, err := regular(path, true)
	if err != nil {
		t.Fatal(err)
	}
	var archived Observation
	if err = observation.Decode(b, &archived, true); err != nil || archived.Phase != "project" || archived.Revision != p.Parent || archived.Gate.State != gateRejected || archived.Runtime != "UNPROVEN" {
		t.Fatal(archived, err)
	}
}
func TestSourceReadFailureIsNotConvergence(t *testing.T) {
	w, _ := sequenceFixture(t)
	app := Object{"spec": Object{"source": Object{"path": "gitops/leaf"}}}
	if equal, err := sourceEqual(context.Background(), w, app, Files{}, strings.Repeat("f", 40)); err == nil || equal {
		t.Fatal("missing source was treated as a comparable tree")
	}
}
