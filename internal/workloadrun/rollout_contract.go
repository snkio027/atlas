package workloadrun

import (
	"atlas-refactor/internal/observation"
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"atlas-refactor/internal/workload"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Compile a read contract from the same receipt chain that fences publication.
// No caller-supplied flag can make an old/missing object "new".
func (w *Workflow) rolloutContract(ctx context.Context, result workload.Result, revision string) (*rolloutSession, error) {
	p, err := w.ReadPlan()
	if err != nil {
		return nil, err
	}
	if p.Schema != 2 || p.ClusterUID == "" || p.ClusterUID != w.Install.Record.ClusterUID || !fullSHA.MatchString(p.Parent) || !fullSHA.MatchString(p.BaseCommit) || !fullSHA.MatchString(revision) || !slices.Equal(p.Phases, publicationPhases(p.Parent == p.BaseCommit)) {
		return nil, errors.New("observation plan/target mismatch")
	}
	index := slices.Index(p.Phases, result.Inventory.Phase)
	if index < 0 {
		return nil, errors.New("observation phase outside plan")
	}
	uid, err := w.baselineUIDs()
	if err != nil {
		return nil, err
	}
	accepted := map[string]bool{p.BaseCommit: true, p.Parent: true}
	parent := p.Parent
	active := true
	dir := filepath.Dir(w.publicationPath(p, result.Inventory.Phase))
	for _, marker := range []string{"terminal.json", "final.json"} {
		if _, err := regular(filepath.Join(dir, marker), true); err == nil {
			active = false
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	for i, phase := range p.Phases[:index+1] {
		digest := p.PhaseSHA256[phase]
		if i == index {
			digest = platform.BundleDigest(result.Files)
			if phase != "consumer" && digest != p.PhaseSHA256[phase] {
				return nil, errors.New("compiled observation tree differs from plan")
			}
		}
		receipt, err := w.readPublication(p, phase, parent, digest)
		if err != nil {
			return nil, err
		}
		if i == index && receipt.Commit != revision {
			return nil, errors.New("observation revision differs from receipt")
		}
		accepted[receipt.Commit] = true
		b, err := regular(filepath.Join(dir, phase+"-gate.json"), true)
		if err == nil {
			var gate Observation
			if err = observation.Decode(b, &gate, true); err != nil {
				return nil, err
			}
			if err = mergeGateUIDs(uid, gate, receipt, p.ClusterUID); err != nil {
				return nil, err
			}
			if i == index {
				active = false
			}
		} else if !os.IsNotExist(err) || i < index {
			return nil, fmt.Errorf("prior Gate evidence required for %s: %w", phase, err)
		}
		if i < index {
			parent = receipt.Commit
		}
	}
	before, err := w.ReadTree(ctx, parent)
	if err != nil {
		return nil, err
	}
	inv, err := workload.InventoryOf(before, w.Context.ResourceModel)
	if err != nil {
		return nil, err
	}
	prior, err := desiredObjects(before, inv)
	if err != nil {
		return nil, err
	}
	current, err := desiredObjects(result.Files, result.Inventory.Resources)
	if err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, r := range result.Inventory.Resources {
		o := current[r.Identity]
		if o["apiVersion"] == "argoproj.io/v1alpha1" && o["kind"] == "Application" {
			owners[str(at(o, "metadata", "name"))] = r.Owner
		}
	}
	return newRolloutSession(active, prior, owners, uid, accepted), nil
}

func (w *Workflow) readPublication(p Plan, phase, parent, digest string) (Publication, error) {
	var receipt Publication
	b, err := regular(w.publicationPath(p, phase), true)
	if err != nil {
		return receipt, err
	}
	if err = workload.StrictDecode(b, &receipt); err != nil {
		return receipt, err
	}
	if receipt.Schema != 1 || receipt.PlanSHA256 != workload.Digest(workload.JSON(p)) || receipt.Phase != phase || receipt.Parent != parent || !fullSHA.MatchString(receipt.Commit) || receipt.TreeSHA256 != digest || digest == "" {
		return receipt, errors.New("publication receipt mismatch: " + phase)
	}
	return receipt, nil
}

// Successful Gates carry newly introduced identities forward. A push receipt
// alone cannot prove readiness or preserve a UID across phases.
func mergeGateUIDs(uid map[string]string, gate Observation, receipt Publication, cluster string) error {
	if gate.Schema != 1 || gate.Phase != receipt.Phase || gate.Revision != receipt.Commit || gate.ClusterUID != cluster || len(gate.UID) == 0 || len(gate.ApplicationFacts) == 0 || gate.Gate != nil && gate.Gate.State != gateReady {
		return errors.New("phase Gate binding/decision differs")
	}
	if gate.Phase != "permissions" && gate.Project != "VERIFIED" || gate.Phase == "consumer" && (gate.Workload != "VERIFIED" || gate.Binding != "VERIFIED") {
		return errors.New("phase Gate incomplete")
	}
	apps := map[string]bool{}
	for _, f := range gate.ApplicationFacts {
		if f.Ref.APIVersion != "argoproj.io/v1alpha1" || f.Ref.Kind != "Application" || f.Ref.Namespace != platformnames.Argo || f.Ref.Name == "" || apps[f.Ref.Name] || f.Classification != observation.Verified || f.UID == "" || f.ResourceVersion == "" || !f.ArgoSSA || !fullSHA.MatchString(f.ObservedRevision) || f.ExpectedRevision != f.ObservedRevision || !observation.Hash(f.ObservedSpecSHA256) || f.ExpectedSpecSHA256 != f.ObservedSpecSHA256 || gate.UID[appIdentity(f.Ref.Name)] != f.UID || gate.Applications[f.Ref.Name] != f.ObservedRevision {
			return errors.New("phase Application proof incomplete")
		}
		apps[f.Ref.Name] = true
	}
	if len(apps) != len(gate.Applications) || len(gate.UID) != len(apps)+len(gate.Resources) {
		return errors.New("phase identity inventory incomplete")
	}
	for id, digest := range gate.Resources {
		if !observation.Hash(digest) || gate.UID[id] == "" {
			return errors.New("phase resource proof incomplete")
		}
	}

	for id, v := range gate.UID {
		if v == "" || uid[id] != "" && uid[id] != v {
			return errors.New("phase UID continuity differs: " + id)
		}
	}
	for id, v := range gate.UID {
		uid[id] = v
	}
	return nil
}

// Standalone publish also persists the successful predecessor Gate; Deploy has
// already saved it. Existing immutable evidence is never replaced.
func (w *Workflow) retainGate(p Plan, report Observation) error {
	path := filepath.Join(filepath.Dir(w.publicationPath(p, report.Phase)), report.Phase+"-gate.json")
	if _, err := regular(path, true); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return save(path, workload.JSON(report), true)
}
