package ot1

import (
	"context"
	"errors"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"path/filepath"
)

func ReadPlan(ctx context.Context, root, repo, path string) (Plan, error) {
	var p Plan
	b, e := observation.RegularPrivate(path)
	if e != nil {
		return p, e
	}
	if e = observation.Decode(b, &p, true); e != nil {
		return p, e
	}
	if string(b) != string(observation.Bytes(p)) {
		return p, errors.New("plan must use canonical create-only encoding")
	}
	scope, stages, e := LoadContracts(root)
	if e != nil {
		return p, e
	}
	if e = ValidatePlan(p, stages, scope); e != nil {
		return p, e
	}
	return p, VerifyRepositoryPlan(ctx, repo, p)
}
func DesiredObjects(ctx context.Context, repo string, plan Plan) (map[string]observation.Object, error) {
	out := map[string]observation.Object{}
	d := DefaultProject()
	out[observation.Reference(d).Key()] = d
	for _, path := range []string{"gitops/platform/foundation/capabilities/overlays/development/resources.json", ProjectsPath, "platform/development/bootstrap/project.json"} {
		b, e := sourceFile(ctx, repo, plan.BaselineRevision, path)
		if e != nil {
			return nil, e
		}
		objects, e := platform.DecodeJSONManifests(b)
		if e != nil {
			return nil, e
		}
		for _, o := range objects {
			key := observation.Reference(o).Key()
			if out[key] != nil {
				return nil, errors.New("duplicate desired identity")
			}
			out[key] = o
		}
	}
	return out, nil
}

// Replay evaluates a complete private input bundle into a NEW attempt. Missing
// stages become a preserved STOP, never an implicit skip or automatic resume.
func Replay(plan Plan, input, output string, desired map[string]observation.Object) (int, error) {
	if plan.Continuation != nil {
		return 2, errors.New("continuation replay requires predecessor bundle; baseline replay is forbidden")
	}
	a, e := NewAttempt(plan, output)
	if e != nil {
		return 2, e
	}
	for i, phase := range plan.Phases {
		stem := stageStem(i, phase.Stage.Name)
		b, e := observation.RegularPrivate(filepath.Join(input, stem+"-snapshot.json"))
		if e != nil {
			_ = a.Stop("SNAPSHOT_UNAVAILABLE")
			return 2, e
		}
		var s Snapshot
		if e = observation.Decode(b, &s, true); e != nil {
			_ = a.Stop("INVALID_SNAPSHOT")
			return 2, e
		}
		var gate *GateProof
		if phase.Stage.AtlasGate {
			gate, e = LoadGateEvidence(input, stem, plan, phase, s)
			if e != nil {
				_ = a.Stop("GATE_EVIDENCE_UNAVAILABLE_OR_INVALID")
				return 2, e
			}
			for name := range gate.EvidenceFiles {
				raw, e := observation.RegularPrivate(filepath.Join(input, stem+"-"+name))
				if e != nil {
					_ = a.Stop("GATE_EVIDENCE_CHANGED")
					return 2, e
				}
				if e = observation.CreatePrivate(filepath.Join(output, stem+"-"+name), raw); e != nil {
					return 2, e
				}
			}
		}
		cp, e := a.Record(s, desired, gate)
		if e != nil {
			return 2, e
		}
		if !cp.Assessment.Passed() {
			return 1, nil
		}
	}
	return 0, nil
}

func ReadSnapshot(path string) (Snapshot, error) {
	var s Snapshot
	b, e := observation.RegularPrivate(path)
	if e != nil {
		return s, e
	}
	e = observation.Decode(b, &s, true)
	return s, e
}
func SavePlan(path string, plan Plan) error {
	if _, e := os.Lstat(path); !os.IsNotExist(e) {
		return errors.New("plan path already exists")
	}
	return observation.CreatePrivate(path, observation.Bytes(plan))
}
