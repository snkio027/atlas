package ot1

import (
	"errors"
	"github.com/snkio027/atlas/internal/observation"
	"reflect"
)

type Step struct {
	RefreshApplications []string `json:"refreshApplications,omitempty"`
	Stage               string   `json:"stage"`
	PublishRevision     string   `json:"publishRevision,omitempty"`
	Release             []string `json:"release,omitempty"`
	Owner               string   `json:"owner,omitempty"`
	Mode                string   `json:"mode,omitempty"`
	Create              bool     `json:"create,omitempty"`
	Sync                bool     `json:"sync,omitempty"`
	AtlasGate           bool     `json:"atlasGate"`
}

// Steps is the complete finite request schedule. It has no shell commands,
// arbitrary resource selectors, retries, automatic rollback or cleanup branch.
func Steps(plan Plan) []Step {
	steps := []Step{}
	for i, p := range plan.Phases {
		s := Step{Stage: p.Stage.Name, AtlasGate: p.Stage.AtlasGate, RefreshApplications: publicationApps(plan, i)}
		if i > 0 {
			before := plan.Phases[i-1]
			if before.Revision != p.Revision {
				s.PublishRevision = p.Revision
			}
			if p.Stage.Outcome == "released" {
				for _, owner := range append([]string{Source}, Targets...) {
					if before.Stage.Applications[owner] != "" {
						s.Release = append(s.Release, owner)
					}
				}
			}
			if p.Stage.ActiveOwner != nil {
				s.Owner = *p.Stage.ActiveOwner
				s.Mode = p.Stage.Applications[s.Owner]
				s.Create = before.Stage.Applications[s.Owner] == ""
				s.Sync = true
			}
		}
		steps = append(steps, s)
	}
	return steps
}
func baseGuards(current, expected observation.Object, uid, revision string) ([]observation.Object, error) {
	ref := observation.Reference(current)
	if destination(ref.Name) == "" || ref != AppRef(ref.Name) || observation.Reference(expected) != ref || uid == "" || observation.String(observation.At(current, "metadata", "uid")) != uid || observation.String(observation.At(current, "metadata", "resourceVersion")) == "" || !observation.FullSHA(revision) {
		return nil, errors.New("Application identity/concurrency fence failed")
	}
	if current["operation"] != nil || observation.At(current, "metadata", "deletionTimestamp") != nil || len(observation.Slice(observation.At(current, "metadata", "finalizers"))) != 0 || len(observation.Slice(observation.At(current, "metadata", "ownerReferences"))) != 0 {
		return nil, errors.New("Application is active or has a deletion boundary")
	}
	phase := observation.String(observation.At(current, "status", "operationState", "phase"))
	if phase == "Running" || phase == "Terminating" {
		return nil, errors.New("Application operation active")
	}
	if observation.Digest(current["spec"]) != observation.Digest(expected["spec"]) || observation.String(observation.At(current, "status", "sync", "revision")) != revision {
		return nil, errors.New("Application full spec/revision fence failed")
	}
	return []observation.Object{{"op": "test", "path": "/metadata/uid", "value": uid}, {"op": "test", "path": "/metadata/resourceVersion", "value": observation.At(current, "metadata", "resourceVersion")}, {"op": "test", "path": "/spec", "value": observation.Clone(observation.Map(current["spec"]))}}, nil
}

// ModePatch is the only spec mutation prepared by the ceremony: exact tests,
// then a single syncOptions replacement. Argo alone mutates resource tracking.
func ModePatch(current observation.Object, uid string, previous, next Phase) ([]observation.Object, error) {
	if next.Stage.ActiveOwner == nil || previous.Stage.ActiveOwner == nil || *previous.Stage.ActiveOwner != *next.Stage.ActiveOwner || next.Stage.Previous == nil || *next.Stage.Previous != previous.Stage.Name || previous.Revision != next.Revision {
		return nil, errors.New("mode transition is not consecutive for one owner")
	}
	owner := *next.Stage.ActiveOwner
	before := previous.Stage.Applications[owner]
	after := next.Stage.Applications[owner]
	if !(before == "strict" && after == "window" && previous.Stage.Outcome == "blocked" || before == "window" && after == "strict" && previous.Stage.Outcome == "success") {
		return nil, errors.New("invalid mode transition")
	}
	expected, _ := Application(owner, before == "strict")
	patches, e := baseGuards(current, expected, uid, previous.Revision)
	if e != nil {
		return nil, e
	}
	if e = operationMatches(current, previous, previous.Stage.Name, previous.Stage.Outcome); e != nil {
		return nil, e
	}
	if previous.Stage.Outcome == "blocked" {
		if e = refusalComparison(current, previous.Revision); e != nil {
			return nil, e
		}
	} else {
		fresh, err := comparisonAfterOperation(current)
		if err != nil || !fresh || len(observation.Slice(observation.At(current, "status", "conditions"))) != 0 || observation.String(observation.At(current, "status", "sync", "status")) != "Synced" || observation.String(observation.At(current, "status", "health", "status")) != "Healthy" {
			return nil, errors.New("previous successful sync comparison not ready")
		}
	}
	return append(patches, observation.Object{"op": "replace", "path": "/spec/syncPolicy/syncOptions", "value": Options(after == "strict")}), nil
}
func SyncPatch(current observation.Object, uid string, phase Phase) ([]observation.Object, error) {
	if phase.Stage.ActiveOwner == nil {
		return nil, errors.New("stage has no sync authority")
	}
	owner := *phase.Stage.ActiveOwner
	expected, e := Application(owner, phase.Stage.Applications[owner] == "strict")
	if e != nil {
		return nil, e
	}
	patches, e := baseGuards(current, expected, uid, phase.Revision)
	if e != nil {
		return nil, e
	}
	if observation.Reference(current).Name != owner {
		return nil, errors.New("wrong stage owner")
	}
	// No resubmission of an operation bearing this stage marker, regardless of
	// outcome. A lost client response is an indeterminate attempt, never a retry.
	info := []any{observation.Object{"name": "ot1-stage", "value": phase.Stage.Name}}
	if reflect.DeepEqual(observation.Slice(observation.At(current, "status", "operationState", "operation", "info")), info) {
		return nil, errors.New("stage operation already submitted")
	}
	op := observation.Object{"initiatedBy": observation.Object{"username": "atlas-ot1-reviewed-ceremony"}, "info": info, "sync": observation.Object{"revision": phase.Revision, "prune": false, "syncOptions": Options(phase.Stage.Applications[owner] == "strict")}}
	return append(patches, observation.Object{"op": "add", "path": "/operation", "value": op}), nil
}
func CreateApplication(phase Phase, owner string, confirmedAbsent bool) (observation.Object, error) {
	if !confirmedAbsent || phase.Stage.ActiveOwner == nil || *phase.Stage.ActiveOwner != owner || phase.Stage.Outcome != "blocked" || phase.Stage.Applications[owner] != "strict" {
		return nil, errors.New("create requires confirmed absence in the precise strict-refusal stage")
	}
	return Application(owner, true)
}

// DeleteRequest returns a DELETE body with atomic UID/RV preconditions and an
// exact Application URL. Plain kubectl delete has no resource-version fence.
func DeleteRequest(current, expected observation.Object, uid, revision string) (string, observation.Object, error) {
	if _, e := baseGuards(current, expected, uid, revision); e != nil {
		return "", nil, e
	}
	ref := observation.Reference(current)
	return "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/" + ref.Name, observation.Object{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Orphan", "preconditions": observation.Object{"uid": uid, "resourceVersion": observation.At(current, "metadata", "resourceVersion")}}, nil
}
