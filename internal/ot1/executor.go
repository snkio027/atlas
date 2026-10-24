package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"strings"
	"time"
)

// Executor is intentionally specific to the compiled OT-1 plan. It cannot
// accept arbitrary object manifests, commands, field paths, branches or targets.
type Executor struct {
	Plan              Plan
	Repository        string // private prepared seven-revision clone
	RuntimeRepository string // the private checkout that actually created OT-1
	ToolDirectory     string
	EvidenceDirectory string
	Reader            observation.InventoryReader
	Runner            atlas.Runner
	Desired           map[string]observation.Object
	request           int
	baseline          *Snapshot
	baselineAudit     map[string]bool
	activeUID         string // captured by the guarded request, including newly created owners
}

func (x *Executor) remote(ctx context.Context) (string, error) {
	out, e := x.Runner.Run(ctx, atlas.Request{Tool: "git", Args: []string{"ls-remote", "--exit-code", x.Plan.Repository, "refs/heads/" + x.Plan.Branch}})
	if e != nil {
		return "", e
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[1] != "refs/heads/"+x.Plan.Branch || !observation.FullSHA(fields[0]) {
		return "", errors.New("Git authority unavailable/ambiguous")
	}
	return fields[0], nil
}
func (x *Executor) fence(ctx context.Context, revision string) error {
	uid, e := x.Reader.ClusterIdentity(ctx)
	if e != nil || uid != x.Plan.Target.ClusterUID {
		return errors.New("cluster binding changed before request")
	}
	b, e := observation.RegularPrivate(filepath.Join(x.RuntimeRepository, ".state/kubeconfig"))
	if e != nil || observation.SHA(b) != x.Plan.Target.KubeconfigSHA256 {
		return errors.New("runtime checkout has a different kubeconfig")
	}
	sha, e := x.remote(ctx)
	if e != nil || sha != revision {
		return errors.New("Git authority moved outside the phase")
	}
	return nil
}
func (x *Executor) recordRequest(ctx context.Context, stage string, q atlas.Request) ([]byte, error) {
	n := x.request
	x.request++
	prefix := filepath.Join(x.EvidenceDirectory, fmt.Sprintf("request-%03d", n))
	intent := observation.Object{"stage": stage, "tool": q.Tool, "args": q.Args, "inputSHA256": observation.SHA(q.Input), "planSHA256": observation.Digest(x.Plan), "time": time.Now().UTC()}
	if e := observation.CreatePrivate(prefix+"-intent.json", observation.Bytes(intent)); e != nil {
		return nil, e
	}
	out, e := x.Runner.Run(ctx, q)
	code := 0
	if e != nil {
		code = 1
	}
	saveErr := observation.CreatePrivate(prefix+"-result.json", observation.Bytes(observation.Object{"exitCode": code, "outputSHA256": observation.SHA(out), "finishedAt": time.Now().UTC()}))
	if e != nil {
		return nil, e
	}
	return out, saveErr
}
func (x *Executor) kube(ctx context.Context, index int, args []string, input []byte) ([]byte, error) {
	phase := x.Plan.Phases[index]
	if e := x.guardObjects(ctx, index); e != nil {
		return nil, e
	}
	if e := x.fence(ctx, phase.Revision); e != nil {
		return nil, e
	}
	prefix := []string{"--kubeconfig", filepath.Join(x.RuntimeRepository, ".state/kubeconfig"), "--context", x.Plan.Target.Context, "--request-timeout=30s"}
	return x.recordRequest(ctx, phase.Stage.Name, atlas.Request{Tool: "kubectl", Args: append(prefix, args...), Input: input})
}
func (x *Executor) wait(ctx context.Context, fn func() (bool, error)) error {
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		done, e := fn()
		if e == nil {
			e = ctx.Err()
		}
		if e != nil || done {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
func (x *Executor) app(ctx context.Context, name string) (observation.Object, error) {
	return x.Reader.Read(ctx, AppRef(name))
}
func (x *Executor) compared(ctx context.Context, index int, name string, expected observation.Object, uid string, parent bool) (observation.Object, error) {
	var out observation.Object
	e := x.wait(ctx, func() (bool, error) {
		o, e := x.app(ctx, name)
		if e != nil || o == nil {
			return false, errors.New("Application unavailable while awaiting comparison")
		}
		if observation.String(observation.At(o, "metadata", "uid")) != uid || observation.Digest(o["spec"]) != observation.Digest(expected["spec"]) || observation.At(o, "metadata", "deletionTimestamp") != nil || len(observation.Slice(observation.At(o, "metadata", "finalizers"))) != 0 {
			return false, errors.New("Application changed while awaiting comparison")
		}
		for _, raw := range observation.Slice(observation.At(o, "status", "conditions")) {
			if parent || observation.String(observation.Map(raw)["type"]) != "SharedResourceWarning" {
				return false, errors.New("unexpected Application condition")
			}
		}
		opPhase := observation.String(observation.At(o, "status", "operationState", "phase"))
		if parent {
			if opPhase == "Failed" || opPhase == "Error" || opPhase == "Terminating" {
				return false, errors.New("parent operation failed or is terminating")
			}
			// Prune=confirm may leave the parent Running. Comparing the precise
			// detached Git SHA authorizes only our guarded child release, not a
			// generic prune approval or a parent/controller mutation.
			if opPhase == "Running" && observation.String(observation.At(o, "status", "operationState", "operation", "sync", "revision")) != x.Plan.Phases[index].Revision {
				return false, errors.New("parent operation revision escaped detached projection")
			}
			if o["operation"] != nil && observation.String(observation.At(o, "operation", "sync", "revision")) != x.Plan.Phases[index].Revision {
				return false, errors.New("parent requested an unplanned revision")
			}
		} else if o["operation"] != nil || opPhase == "Running" || opPhase == "Terminating" {
			return false, nil
		}
		if observation.String(observation.At(o, "status", "sync", "revision")) != x.Plan.Phases[index].Revision {
			return false, nil
		}
		out = o
		return true, nil
	})
	return out, e
}
func (x *Executor) publish(ctx context.Context, index int, previous Snapshot) error {
	phase := x.Plan.Phases[index]
	if e := x.fence(ctx, previous.Revision); e != nil {
		return e
	}
	// Only a normal fast-forward push. Exact before/after branch fences plus the
	// immutable seven-commit plan deny force, arbitrary refs and dev02 publication.
	if _, e := x.recordRequest(ctx, phase.Stage.Name, atlas.Request{Tool: "git", Args: []string{"-C", x.Repository, "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "credential.https://github.com.helper=!gh auth git-credential", "push", x.Plan.Repository, phase.Revision + ":refs/heads/" + x.Plan.Branch}}); e != nil {
		return e
	}
	if e := x.fence(ctx, phase.Revision); e != nil {
		return e
	}
	// Import known local objects into the existing private runtime checkout;
	// its ignored kubeconfig/image/audit state stays with the original substrate.
	for _, args := range [][]string{{"fetch", "--no-tags", x.Repository, phase.Revision}, {"checkout", "--detach", phase.Revision}} {
		if _, e := x.recordRequest(ctx, phase.Stage.Name, atlas.Request{Tool: "git", Args: append([]string{"-c", "core.hooksPath=/dev/null"}, args...)}); e != nil {
			return e
		}
	}
	if x.Plan.PublicationRefresh {
		return x.requestNormalRefreshForPublishedRevision(ctx, index, previous)
	}
	return nil
}
func (x *Executor) Transition(ctx context.Context, index int, previous *Snapshot) error {
	x.activeUID = ""
	phase := x.Plan.Phases[index]
	step := Steps(x.Plan)[index]
	if index == 0 {
		audit, e := readAudit(x.RuntimeRepository)
		if e != nil {
			return e
		}
		x.baselineAudit = allAuditIDs(audit)
		return x.fence(ctx, phase.Revision)
	}
	if previous == nil {
		return errors.New("verified predecessor is required")
	}
	if e := x.fence(ctx, previous.Revision); e != nil {
		return e
	}
	fresh, e := x.captureCoherent(ctx, index-1, x.baseline, previous)
	if e != nil {
		return e
	}
	if checked := Assess(x.Plan, index-1, fresh, x.baseline, previous, x.Desired, nil); checked.Ownership != "VERIFIED" {
		return errors.New("predecessor changed before mutation")
	}
	if step.PublishRevision != "" {
		if e := x.publish(ctx, index, *previous); e != nil {
			return e
		}
	}
	if len(step.Release) > 0 {
		// The parent must compare the detached Git projection before release. Its
		// previous UID/full spec is fenced; no generic prune confirmation is issued.
		parent := rawIndex(previous.Applications)[AppRef("platform-control").Key()]
		if _, e := x.compared(ctx, index, "platform-control", parent, observation.String(observation.At(parent, "metadata", "uid")), true); e != nil {
			return e
		}
		for _, name := range step.Release {
			old := rawIndex(previous.Applications)[AppRef(name).Key()]
			uid := observation.String(observation.At(old, "metadata", "uid"))
			current, e := x.compared(ctx, index, name, old, uid, false)
			if e != nil {
				return e
			}
			url, body, e := DeleteRequest(current, old, uid, phase.Revision)
			if e != nil {
				return e
			}
			if _, e = x.kube(ctx, index, []string{"delete", "--raw=" + url, "-f", "-"}, observation.Bytes(body)); e != nil {
				return e
			}
			if e = x.wait(ctx, func() (bool, error) { o, e := x.app(ctx, name); return o == nil && e == nil, e }); e != nil {
				return e
			}
		}
	}
	if step.Owner == "" {
		return nil
	}
	var current observation.Object
	uid := ""
	if step.Create {
		absent, e := x.app(ctx, step.Owner)
		if e != nil || absent != nil {
			return errors.New("new owner absence not confirmed")
		}
		definition, e := CreateApplication(phase, step.Owner, true)
		if e != nil {
			return e
		}
		b, e := x.kube(ctx, index, []string{"create", "-f", "-", "-o", "json"}, observation.Bytes(definition))
		if e != nil {
			return e
		}
		if e = observation.Decode(b, &current, false); e != nil {
			return e
		}
		uid = observation.String(observation.At(current, "metadata", "uid"))
		current, e = x.compared(ctx, index, step.Owner, definition, uid, false)
		if e != nil {
			return e
		}
	} else {
		old := rawIndex(previous.Applications)[AppRef(step.Owner).Key()]
		uid = observation.String(observation.At(old, "metadata", "uid"))
		var e error
		current, e = x.app(ctx, step.Owner)
		if e != nil {
			return e
		}
		patch, e := ModePatch(current, uid, x.Plan.Phases[index-1], phase)
		if e != nil {
			return e
		}
		if _, e = x.kube(ctx, index, []string{"patch", "application", step.Owner, "-n", "argocd", "--type=json", "-p", string(observation.Bytes(patch))}, nil); e != nil {
			return e
		}
		expected, _ := Application(step.Owner, step.Mode == "strict")
		current, e = x.compared(ctx, index, step.Owner, expected, uid, false)
		if e != nil {
			return e
		}
	}
	x.activeUID = uid
	patch, e := SyncPatch(current, uid, phase)
	if e != nil {
		return e
	}
	_, e = x.kube(ctx, index, []string{"patch", "application", step.Owner, "-n", "argocd", "--type=json", "-p", string(observation.Bytes(patch))}, nil)
	return e
}
func (x *Executor) Converge(ctx context.Context, index int, baseline, previous *Snapshot) (Snapshot, error) {
	var out Snapshot
	e := x.wait(ctx, func() (bool, error) {
		phase := x.Plan.Phases[index]
		if e := x.fence(ctx, phase.Revision); e != nil {
			return false, e
		}
		// Avoid opening a multi-object version fence while our own operation is
		// still changing Application status. A single list is only a readiness
		// precheck; the complete closing fences below remain mandatory.
		apps, e := x.Reader.List(ctx, AppRef(""))
		if e != nil {
			return false, e
		}
		out = Snapshot{Schema: 1, PlanSHA256: observation.Digest(x.Plan), Stage: phase.Stage.Name, Revision: phase.Revision, Applications: apps, InventoryError: "READINESS_PRECHECK_NOT_FULL_CAPTURE"}
		ready, e := applicationProgress(x.Plan, index, apps, previous, x.activeUID)
		if e != nil || !ready {
			return false, e
		}
		s, e := x.captureCoherent(ctx, index, baseline, previous)
		out = s
		if e != nil {
			return false, e
		}
		if _, e = applicationProgress(x.Plan, index, s.Applications, previous, x.activeUID); e != nil {
			return false, e
		}
		if e = x.fence(ctx, phase.Revision); e != nil {
			return false, e
		}
		assessed := Assess(x.Plan, index, s, baseline, previous, x.Desired, nil)
		if assessed.Ownership == "VERIFIED" {
			if e = x.auditScope(); e != nil {
				return false, e
			}
			if e = ctx.Err(); e != nil {
				return false, e
			}
			if index == 0 {
				copy := s
				x.baseline = &copy
			}
			return true, nil
		}
		if e := transientOnly(x.Plan, index, s, baseline, previous, x.Desired); e != nil {
			return false, e
		}
		return false, nil
	})
	if e != nil {
		_ = observation.CreatePrivate(filepath.Join(x.EvidenceDirectory, stageStem(index, x.Plan.Phases[index].Stage.Name)+"-failed-observation.json"), observation.Bytes(out))
	}
	return out, e
}

// Expected progress is narrow: unchanged objects with predecessor/current owner,
// healthy platform Apps comparing to the new SHA, and the single requested Argo
// operation running. Unavailable reads and unrelated drift always stop.
func transientOnly(plan Plan, index int, s Snapshot, baseline, previous *Snapshot, desired map[string]observation.Object) error {
	if s.InventoryError != "" || len(s.Envelope.Reasons) > 0 {
		return errors.New("observation unavailable or changed during capture")
	}
	phase := plan.Phases[index]
	step := Steps(plan)[index]
	if step.Sync {
		active := rawIndex(s.Applications)[AppRef(step.Owner).Key()]
		opPhase := observation.String(observation.At(active, "status", "operationState", "phase"))
		marker := observation.Digest(observation.At(active, "status", "operationState", "operation", "info"))
		if active["operation"] == nil && (opPhase == "Succeeded" || opPhase == "Failed") && marker == observation.Digest([]any{observation.Object{"name": "ot1-stage", "value": phase.Stage.Name}}) {
			return errors.New("terminal operation did not satisfy the full phase contract")
		}
	}
	if _, e := applicationProgress(plan, index, s.Applications, previous, ""); e != nil {
		return e
	}
	return convergenceSafety(plan, index, s.Envelope.Raw, s.Projects, baseline, previous, desired)
}

func convergenceSafety(plan Plan, index int, objects, projects []observation.Object, baseline, previous *Snapshot, desired map[string]observation.Object) error {
	if baseline == nil {
		return errors.New("baseline is not already fully verified")
	}
	phase := plan.Phases[index]
	base := rawIndex(baseline.Envelope.Raw)
	raw := rawIndex(objects)
	if len(raw) != len(objects) || len(raw) != len(phase.Applications)+17 {
		return errors.New("transient inventory differs from the plan")
	}
	for _, item := range plan.Scope.Objects {
		ref, _ := ParseIdentity(item.Identity)
		o := raw[ref.Key()]
		b := base[ref.Key()]
		want := observation.ExpectedResource{Ref: ref, UID: observation.String(observation.At(b, "metadata", "uid")), SemanticSHA256: observation.Digest(observation.Semantic(b)), RequireSSA: true, Readiness: readiness(ref)}
		if observation.ClassifyResource(want, o, nil).Classification != observation.Verified {
			return errors.New("resource changed during convergence")
		}
		tracking := observation.String(observation.At(o, "metadata", "annotations", observation.TrackingAnnotation))
		expected := observation.Tracking(ownerFor(phase.Stage, item), destination(ownerFor(phase.Stage, item)), ref)
		if tracking != expected {
			if previous == nil || phase.Stage.Outcome != "success" || phase.Stage.ActiveOwner == nil || phase.Stage.Applications[*phase.Stage.ActiveOwner] != "window" {
				return errors.New("unexpected ownership during convergence")
			}
			prior := plan.Phases[index-1].Stage
			if tracking != observation.Tracking(ownerFor(prior, item), destination(ownerFor(prior, item)), ref) {
				return errors.New("ownership escaped the planned window")
			}
		}
	}
	for _, ref := range identityRefs() {
		b, o := base[ref.Key()], raw[ref.Key()]
		if o == nil || observation.String(observation.At(b, "metadata", "uid")) != observation.String(observation.At(o, "metadata", "uid")) || observation.Digest(observation.Semantic(b)) != observation.Digest(observation.Semantic(o)) {
			return errors.New("Bootstrap invariant changed during convergence")
		}
	}
	return checkProjects(projects, baseline, desired)
}

// applicationProgress checks one list snapshot without pretending that it is
// complete ownership evidence. Only known controller progress may be waited on.
// activeUID binds a newly created owner to the UID returned before SyncPatch.
func applicationProgress(plan Plan, index int, applications []observation.Object, previous *Snapshot, activeUID string) (bool, error) {
	phase := plan.Phases[index]
	step := Steps(plan)[index]
	ready := true
	apps := rawIndex(applications)
	if len(apps) != len(phase.Applications) || len(apps) != len(applications) {
		return false, errors.New("Application inventory changed during convergence")
	}
	prev := map[string]observation.Object{}
	if previous != nil {
		prev = rawIndex(previous.Applications)
	}
	for _, expect := range phase.Applications {
		o := apps[AppRef(expect.Name).Key()]
		old := prev[AppRef(expect.Name).Key()]
		if o == nil || observation.String(observation.At(o, "metadata", "uid")) == "" || old != nil && observation.String(observation.At(old, "metadata", "uid")) != observation.String(observation.At(o, "metadata", "uid")) || observation.At(o, "metadata", "deletionTimestamp") != nil || len(observation.Slice(observation.At(o, "metadata", "finalizers"))) > 0 {
			return false, errors.New("Application identity/deletion drift during convergence")
		}
		spec := observation.Digest(o["spec"])
		if spec != observation.Digest(expect.Spec) && !(phase.Stage.AtlasGate && destination(expect.Name) != "" && spec == observation.Digest(old["spec"])) {
			return false, errors.New("Application spec drift during convergence")
		}
		syncOwner := step.Sync && expect.Name == step.Owner
		if syncOwner && activeUID != "" && observation.String(observation.At(o, "metadata", "uid")) != activeUID {
			return false, errors.New("requested owner UID changed during convergence")
		}
		for _, c := range observation.Slice(observation.At(o, "status", "conditions")) {
			if !(syncOwner && observation.String(observation.Map(c)["type"]) == "SharedResourceWarning") {
				return false, errors.New("unexpected Argo condition")
			}
		}
		health := observation.String(observation.At(o, "status", "health", "status"))
		if health == "" || health == "Unknown" || health == "Degraded" {
			return false, errors.New("unhealthy/unknown Application")
		}
		observed := observation.String(observation.At(o, "status", "sync", "revision"))
		if observed != phase.Revision && (previous == nil || observed != previous.Revision) && !desiredEquivalent(plan, index, expect, o, old) {
			return false, errors.New("unexpected Application revision")
		}

		op := observation.String(observation.At(o, "status", "operationState", "phase"))
		if op == "Error" || op == "Terminating" {
			return false, errors.New("unexpected operation error/termination")
		}
		if op == "Failed" {
			if !syncOwner || !strings.Contains(observation.String(observation.At(o, "status", "operationState", "message")), "Shared resource found:") {
				return false, errors.New("unexpected operation failure")
			}
			marker := observation.Digest(observation.At(o, "status", "operationState", "operation", "info"))
			currentMarker := observation.Digest([]any{observation.Object{"name": "ot1-stage", "value": phase.Stage.Name}})
			oldMarker := ""
			if index > 0 {
				oldMarker = observation.Digest([]any{observation.Object{"name": "ot1-stage", "value": plan.Phases[index-1].Stage.Name}})
			}
			if !(phase.Stage.Outcome == "blocked" && marker == currentMarker || phase.Stage.Applications[expect.Name] == "window" && marker == oldMarker) {
				return false, errors.New("failed operation does not belong to the expected refusal")
			}
		}
		if syncOwner && o["operation"] != nil {
			request := observation.Map(o["operation"])
			if observation.Digest(request["info"]) != observation.Digest([]any{observation.Object{"name": "ot1-stage", "value": phase.Stage.Name}}) || !approvedSyncRequest(request, phase.Revision, Options(phase.Stage.Applications[expect.Name] == "strict")) {
				return false, errors.New("active operation escaped the phase request")
			}
		}
		check := expect
		check.Spec = observation.Map(o["spec"])
		if old != nil {
			check.UID = observation.String(observation.At(old, "metadata", "uid"))
		}
		check = transitionExpectation(plan, index, check, o, old)
		fact := observation.ClassifyApplication(check, o, nil)
		for _, reason := range fact.Reasons {
			switch reason {
			case "REVISION_NOT_CONVERGED", "OUT_OF_SYNC", "HEALTH_NOT_READY", "OPERATION_ACTIVE", "HOOK_OPERATION_ACTIVE", "BLOCKING_RESOURCES", "STALE_OBSERVED_GENERATION":
			case "SHARED_RESOURCE", "LAST_OPERATION_FAILED":
				if !syncOwner {
					return false, errors.New("unrelated refusal while awaiting convergence")
				}
			default:
				return false, fmt.Errorf("Application observation cannot converge: %s", reason)
			}
		}
		appReady := spec == observation.Digest(expect.Spec) && fact.Classification == observation.Verified
		if syncOwner {
			if state := observation.Map(observation.At(o, "status", "operationState")); op == "Running" {
				request := observation.Map(state["operation"])
				if observation.Digest(request["info"]) != observation.Digest([]any{observation.Object{"name": "ot1-stage", "value": phase.Stage.Name}}) || !approvedSyncRequest(request, phase.Revision, Options(phase.Stage.Applications[expect.Name] == "strict")) {
					return false, errors.New("running operation escaped the phase request")
				}
			}
			if o["operation"] == nil && (op == "Succeeded" || op == "Failed") {
				if e := operationMatches(o, phase, phase.Stage.Name, phase.Stage.Outcome); e != nil {
					return false, e
				}
				if observed != phase.Revision {
					return false, errors.New("owner comparison revision mismatch")
				}
				if phase.Stage.Outcome != "blocked" {
					fresh, e := comparisonAfterOperation(o)
					if e != nil {
						return false, e
					}
					appReady = appReady && fresh
				} else if e := refusalComparison(o, phase.Revision); e != nil {
					return false, e
				}
				if phase.Stage.Outcome == "blocked" {
					// A strict refusal is ready evidence, never generic health.
					appReady = spec == observation.Digest(expect.Spec)
					for _, reason := range fact.Reasons {
						if reason == "STALE_OBSERVED_GENERATION" {
							appReady = false
						}
					}
				}
			} else {
				appReady = false
			}
		}
		ready = ready && appReady
	}
	return ready, nil
}

func (x *Executor) Preflight(ctx context.Context) error {
	if x.Plan.Continuation != nil {
		return errors.New("continuation requires explicit anchor and lock handoff")
	}
	return x.preflightAt(ctx, x.Plan.BaselineRevision)
}
func (x *Executor) preflightAt(ctx context.Context, revision string) error {
	if x.Plan.Target.Cluster != "atlas-refactor-test-ot1" || x.Plan.Branch != "codex/ot1-desired-state" {
		return errors.New("executor target outside OT-1")
	}
	for _, args := range [][]string{{"status", "--porcelain", "--untracked-files=all"}, {"rev-parse", "HEAD"}} {
		b, e := x.Runner.Run(ctx, atlas.Request{Tool: "git", Args: args})
		if e != nil {
			return e
		}
		if args[0] == "status" && len(b) != 0 || args[0] == "rev-parse" && strings.TrimSpace(string(b)) != revision {
			return errors.New("runtime checkout is not the clean reviewed baseline")
		}
	}
	if e := VerifyRepositoryPlan(ctx, x.Repository, x.Plan); e != nil {
		return e
	}
	return x.fence(ctx, revision)
}
func (x *Executor) guardObjects(ctx context.Context, index int) error {
	if index == 0 || x.baseline == nil {
		return errors.New("mutation requires a verified baseline")
	}
	base := rawIndex(x.baseline.Envelope.Raw)
	previous := x.Plan.Phases[index-1].Stage
	for _, item := range x.Plan.Scope.Objects {
		ref, _ := ParseIdentity(item.Identity)
		o, e := x.Reader.Read(ctx, ref)
		if e != nil {
			return e
		}
		old := base[ref.Key()]
		want := observation.ExpectedResource{Ref: ref, UID: observation.String(observation.At(old, "metadata", "uid")), SemanticSHA256: observation.Digest(observation.Semantic(old)), Tracking: observation.Tracking(ownerFor(previous, item), destination(ownerFor(previous, item)), ref), RequireSSA: true, Readiness: readiness(ref)}
		if observation.ClassifyResource(want, o, nil).Classification != observation.Verified {
			return errors.New("ownership/resource precondition changed before mutation")
		}
	}
	return x.auditScope()
}
