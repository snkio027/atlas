//go:build ot1_finalization

package ot1

// This fixed experiment entry is excluded from normal binaries. It cannot choose
// another incident, starting stage, source revision or target.
import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"strings"
)

const FinalizationManifest = "252baa12ed1d3b56d8c92690bf77981270e1f310c7b3ca915e3c427f62ecae68"
const finalizationPlan = "619e492be725fbfb7f3d24aba380479defd64cafad433a107738cf5ab1d65cff"
const finalizationLock = "959051a4bd141acdc59d1bf5373e3e360084ab02f2e508368affc8733f37cd36"
const finalizationGit = "6c1311ff9fa64a80973eb0ef4f4c3ba406ecac24"

type FinalizationIncident struct {
	predecessor *Predecessor
	previous    Snapshot
	lock        []byte
	audit       observation.Object
}

func LoadFinalization(ctx context.Context, root, repo, sourceBundle, stopBundle string) (*FinalizationIncident, error) {
	old, e := LoadPredecessor(ctx, root, repo, sourceBundle, SourceReleasedManifest)
	if e != nil {
		return nil, e
	}
	m, e := verifyBundle(stopBundle, FinalizationManifest)
	if e != nil {
		return nil, e
	}
	if m.PlanSHA256 != finalizationPlan || m.PredecessorManifestSHA256 != "3ada495c8740562e9b06fe4b855b245b6e062c2d48a9ec9c9f6c5c40d2d96f18" {
		return nil, errors.New("stage-23 lineage mismatch")
	}
	plan, e := ReadPlan(ctx, root, repo, filepath.Join(stopBundle, "plan.json"))
	if e != nil {
		return nil, e
	}
	if observation.Digest(plan) != finalizationPlan || old.CheckPlan(plan) != nil || plan.Target.ClusterUID != "b886f730-ea3b-44b9-a904-8bd55ac345f2" || plan.Phases[23].Revision != finalizationGit || m.Files["atlas-ot1-stage12"].SHA256 != plan.Implementation.BinarySHA256 {
		return nil, errors.New("stage-23 plan/target/executable mismatch")
	}
	read := func(name string, dst any) error {
		b, e := bundledFile(stopBundle, name)
		if e != nil {
			return e
		}
		return observation.Decode(b, dst, true)
	}
	var terminal observation.Object
	if e = read("attempt/terminal.json", &terminal); e != nil {
		return nil, e
	}
	if e = finalizationTerminal(terminal); e != nil {
		return nil, e
	}
	previous, e := ReadSnapshot(filepath.Join(stopBundle, "attempt/22-FORWARD_STORAGE_STRICT_RESTORED-snapshot.json"))
	if e != nil {
		return nil, e
	}
	before, e := ReadSnapshot(filepath.Join(stopBundle, "attempt/21-FORWARD_STORAGE_ADOPTED_WINDOW-snapshot.json"))
	if e != nil {
		return nil, e
	}
	var cp Checkpoint
	if e = read("attempt/22-FORWARD_STORAGE_STRICT_RESTORED-checkpoint.json", &cp); e != nil {
		return nil, e
	}
	desired, e := DesiredObjects(ctx, repo, plan)
	if e != nil {
		return nil, e
	}
	if cp.Index != 22 || cp.PlanSHA256 != finalizationPlan || cp.SnapshotSHA256 != observation.Digest(previous) || cp.PreviousSHA256 != observation.Digest(before) || !cp.Assessment.Passed() || !Assess(plan, 22, previous, &old.Baseline, &before, desired, nil).Passed() {
		return nil, errors.New("stage-23 predecessor checkpoint unproven")
	}
	if e = finalizationOwner(previous); e != nil {
		return nil, e
	}
	lock, e := bundledFile(stopBundle, "post-stop/active-stop-lock.json")
	if e != nil || observation.SHA(lock) != finalizationLock {
		return nil, errors.New("stage-23 STOP lock mismatch")
	}
	var anchorAudit GateArtifact
	if e = read("attempt/anchor/12-MIXED_ROLLBACK_VERIFIED-audit-after.json", &anchorAudit); e != nil {
		return nil, e
	}
	if anchorAudit.Kind != "audit-after.json" || anchorAudit.PlanSHA256 != finalizationPlan || anchorAudit.Data["complete"] != true {
		return nil, errors.New("finalization audit anchor mismatch")
	}
	events := observation.Slice(anchorAudit.Data["events"])
	b, e := bundledFile(stopBundle, "post-stop/audit-delta.jsonl")
	if e != nil {
		return nil, e
	}
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var event observation.Object
		if e = observation.Decode(scan.Bytes(), &event, false); e != nil {
			return nil, e
		}
		if event["stage"] == "ResponseComplete" {
			if event["level"] != "Metadata" || event["requestObject"] != nil || event["responseObject"] != nil {
				return nil, errors.New("invalid stage-23 audit")
			}
			events = append(events, event)
		}
	}
	if e = scan.Err(); e != nil {
		return nil, e
	}
	if len(events) == 0 {
		return nil, errors.New("empty stage-23 audit")
	}
	return &FinalizationIncident{old, previous, lock, observation.Object{"events": events}}, nil
}
func finalizationTerminal(terminal observation.Object) error {
	want := observation.Object{"state": "STOP", "nextIndex": 23, "noAutomaticRecovery": true, "planSHA256": finalizationPlan, "reason": "STAGE_FAILED_OR_INTERRUPTED:FORWARD_VERIFIED"}
	if observation.Digest(terminal) != observation.Digest(want) {
		return errors.New("only the exact stage-23 STOP is supported")
	}
	return nil
}
func finalizationOwner(s Snapshot) error {
	apps := rawIndex(s.Applications)
	for name, uid := range map[string]string{
		"secrets-foundation":       "64af31ad-4f72-4e7d-a915-9db9e440c825",
		"observability-foundation": "30aefabc-a93f-4b3d-8e84-4fa1250dafe3",
		"storage-foundation":       "a0a975e4-ec73-4b75-b6e1-22704f1b75b9",
	} {
		if observation.At(apps[AppRef(name).Key()], "metadata", "uid") != uid {
			return errors.New("finalization owner UID mismatch")
		}
	}
	if apps[AppRef(Source).Key()] != nil {
		return errors.New("source owner unexpectedly present")
	}
	return nil
}
func (f *FinalizationIncident) Plan(impl observation.Implementation) (Plan, error) {
	return PrepareContinuation(f.predecessor, impl)
}
func finalizationAuditContinuity(frozen, live observation.Object) error {
	known, current := allAuditIDs(frozen), allAuditIDs(live)
	for id := range known {
		if id == "" || !current[id] {
			return errors.New("stage-23 audit history lost")
		}
	}
	for _, v := range observation.Slice(live["events"]) {
		e := observation.Map(v)
		if !known[observation.String(e["auditID"])] && strings.HasPrefix(observation.String(e["userAgent"]), "kubectl/") {
			return errors.New("operator write after stage-23 STOP")
		}
	}
	return nil
}
func (x *Executor) finalizationFence(ctx context.Context, f *FinalizationIncident) error {
	if e := x.fence(ctx, finalizationGit); e != nil {
		return e
	}
	lock, e := observation.RegularPrivate(filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock"))
	if e != nil || !bytesEqual(lock, f.lock) || observation.SHA(lock) != finalizationLock {
		return errors.New("active stage-23 STOP lock changed")
	}
	if e = x.auditScope(); e != nil {
		return e
	}
	live, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return e
	}
	return finalizationAuditContinuity(f.audit, live)
}

// FinalizationAnchor creates fresh read-only facts, never a historical checkpoint.
func (x *Executor) FinalizationAnchor(ctx context.Context, f *FinalizationIncident, dir string) (Snapshot, error) {
	var empty Snapshot
	if e := f.predecessor.CheckPlan(x.Plan); e != nil {
		return empty, e
	}
	delegate := x.Runner
	guard := &ReadOnlyRunner{Delegate: delegate}
	x.Runner = guard
	defer func() { x.Runner = delegate }()
	if e := x.preflightAt(ctx, finalizationGit); e != nil {
		return empty, e
	}
	x.baseline = &f.predecessor.Baseline
	x.baselineAudit = allAuditIDs(f.predecessor.audit)
	if e := x.finalizationFence(ctx, f); e != nil {
		return empty, e
	}
	s, e := Capture(ctx, x.Reader, x.Plan, 23, x.baseline)
	if e != nil {
		return s, e
	}
	if e = observation.CreatePrivate(filepath.Join(dir, "opening-snapshot.json"), observation.Bytes(s)); e != nil {
		return s, e
	}
	if a := Assess(x.Plan, 23, s, x.baseline, &f.previous, x.Desired, nil); a.Ownership != "VERIFIED" {
		return s, fmt.Errorf("stage-23 anchor: %v", a.Reasons)
	}
	if e = finalizationOwner(s); e != nil {
		return s, e
	}
	latest, gate, e := x.AtlasGate(ctx, 23, s, x.baseline, dir)
	if se := observation.CreatePrivate(filepath.Join(dir, "finalization-anchor-snapshot.json"), observation.Bytes(latest)); se != nil {
		return latest, se
	}
	if e != nil {
		return latest, e
	}
	assessment := Assess(x.Plan, 23, latest, x.baseline, &f.previous, x.Desired, gate)
	if !assessment.Passed() || guard.Denied != 0 {
		return latest, errors.New("stage-23 full Gate-B not verified")
	}
	if e = x.finalizationFence(ctx, f); e != nil {
		return latest, e
	}
	receipt := observation.Object{"stage": "CONTINUATION_ANCHOR_FORWARD_VERIFIED", "purpose": "fresh stage-23 anchor; historical STOP unchanged", "planSHA256": observation.Digest(x.Plan), "predecessorManifestSHA256": FinalizationManifest, "predecessorPlanSHA256": finalizationPlan, "predecessorLockSHA256": finalizationLock, "firstIndex": 24, "snapshotSHA256": observation.Digest(latest), "assessment": assessment, "gate": gate}
	if e = observation.CreatePrivate(filepath.Join(dir, "finalization-anchor.json"), observation.Bytes(receipt)); e != nil {
		return latest, e
	}
	return latest, nil
}
func finalizationAttempt(plan Plan, dir string, baseline *Snapshot) (*Attempt, error) {
	step := Steps(plan)[24]
	if step.Stage != "REVERSE_TARGETS_RELEASED" || step.Owner != "" || step.Create || step.Sync || step.PublishRevision != plan.Revisions["detached-reverse"].Commit || len(step.Release) != 3 || strings.Join(step.Release, ",") != strings.Join(Targets, ",") {
		return nil, errors.New("stage-23 successor must be original reverse target release")
	}
	a, e := NewAttempt(plan, dir)
	if e != nil {
		return nil, e
	}
	a.next = 24
	a.baseline = baseline
	return a, nil
}
func (x *Executor) ExecuteFinalization(ctx context.Context, approved string, f *FinalizationIncident) error {
	if approved != observation.Digest(x.Plan) {
		return errors.New("exact stage-23 plan approval required")
	}
	a, e := finalizationAttempt(x.Plan, x.EvidenceDirectory, &f.predecessor.Baseline)
	if e != nil {
		return e
	}
	anchor, e := x.FinalizationAnchor(ctx, f, filepath.Join(x.EvidenceDirectory, "anchor"))
	if e != nil {
		_ = a.Stop("FINALIZATION_ANCHOR_REJECTED")
		return e
	}
	a.previous = &anchor
	lockPath := filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock")
	successor := observation.Bytes(observation.Object{"planSHA256": approved, "attempt": x.EvidenceDirectory, "predecessorLockSHA256": finalizationLock, "predecessorManifestSHA256": FinalizationManifest, "firstIndex": 24})
	if e = handoffLock(lockPath, f.lock, successor, x.EvidenceDirectory); e != nil {
		_ = a.Stop("LOCK_HANDOFF_FAILED")
		return e
	}
	e = runStages(ctx, a, x.Desired, x, x.baseline, &anchor, 24)
	if e == nil {
		lock, err := observation.RegularPrivate(lockPath)
		if err != nil || !bytesEqual(lock, successor) {
			return errors.New("completed stage-23 successor lock changed")
		}
		e = os.Remove(lockPath)
	}
	return e
}
