//go:build ot1_stage12_continuation

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

const Stage12Manifest = "3ada495c8740562e9b06fe4b855b245b6e062c2d48a9ec9c9f6c5c40d2d96f18"
const stage12Plan = "8aace8f17eeed6736700f07b3e9c9c77258ba2c4f35a8790b45201a636f8b0a2"
const stage12Lock = "d7e4a9d78fc284f55b1e34b3aafe9b67998fd7789eb0201814cec2c280a7b1d2"
const stage12Git = "220a113b0570b781cbfd6415c39c527ede93f9fb"
const stage12OwnerUID = "7c4d4e95-1ea2-4874-8325-ef5ab522cd3a"

type Stage12Incident struct {
	predecessor *Predecessor
	previous    Snapshot
	lock        []byte
	audit       observation.Object
}

func LoadStage12(ctx context.Context, root, repo, sourceBundle, stopBundle string) (*Stage12Incident, error) {
	old, e := LoadPredecessor(ctx, root, repo, sourceBundle, SourceReleasedManifest)
	if e != nil {
		return nil, e
	}
	m, e := verifyBundle(stopBundle, Stage12Manifest)
	if e != nil {
		return nil, e
	}
	if m.PlanSHA256 != stage12Plan || m.PredecessorManifestSHA256 != "57a0e0f74aaa11220a0f37625da0f2b0035060b5faf6e66d6259355534a7a806" {
		return nil, errors.New("stage-12 lineage mismatch")
	}
	plan, e := ReadPlan(ctx, root, repo, filepath.Join(stopBundle, "plan.json"))
	if e != nil {
		return nil, e
	}
	if observation.Digest(plan) != stage12Plan || old.CheckPlan(plan) != nil || plan.Target.ClusterUID != "b886f730-ea3b-44b9-a904-8bd55ac345f2" || plan.Phases[12].Revision != stage12Git || m.Files["atlas-ot1-f12"].SHA256 != plan.Implementation.BinarySHA256 {
		return nil, errors.New("stage-12 plan/target/executable mismatch")
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
	if e = stage12Terminal(terminal); e != nil {
		return nil, e
	}
	previous, e := ReadSnapshot(filepath.Join(stopBundle, "attempt/11-MIXED_ROLLBACK_SOURCE_STRICT_RESTORED-snapshot.json"))
	if e != nil {
		return nil, e
	}
	before, e := ReadSnapshot(filepath.Join(stopBundle, "attempt/10-MIXED_ROLLBACK_SOURCE_READOPTED_WINDOW-snapshot.json"))
	if e != nil {
		return nil, e
	}
	var cp Checkpoint
	if e = read("attempt/11-MIXED_ROLLBACK_SOURCE_STRICT_RESTORED-checkpoint.json", &cp); e != nil {
		return nil, e
	}
	desired, e := DesiredObjects(ctx, repo, plan)
	if e != nil {
		return nil, e
	}
	if cp.Index != 11 || cp.PlanSHA256 != stage12Plan || cp.SnapshotSHA256 != observation.Digest(previous) || cp.PreviousSHA256 != observation.Digest(before) || !cp.Assessment.Passed() || !Assess(plan, 11, previous, &old.Baseline, &before, desired, nil).Passed() {
		return nil, errors.New("stage-12 predecessor checkpoint unproven")
	}
	if e = stage12Owner(previous); e != nil {
		return nil, e
	}
	lock, e := bundledFile(stopBundle, "post-stop/active-stop-lock.json")
	if e != nil || observation.SHA(lock) != stage12Lock {
		return nil, errors.New("stage-12 STOP lock mismatch")
	}
	b, e := bundledFile(stopBundle, "post-stop/audit-events.jsonl")
	if e != nil {
		return nil, e
	}
	events := []any{}
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var event observation.Object
		if e = observation.Decode(scan.Bytes(), &event, false); e != nil {
			return nil, e
		}
		if event["stage"] == "ResponseComplete" {
			if event["level"] != "Metadata" || event["requestObject"] != nil || event["responseObject"] != nil {
				return nil, errors.New("invalid stage-12 audit")
			}
			events = append(events, event)
		}
	}
	if e = scan.Err(); e != nil {
		return nil, e
	}
	if len(events) == 0 {
		return nil, errors.New("empty stage-12 audit")
	}
	return &Stage12Incident{old, previous, lock, observation.Object{"events": events}}, nil
}
func stage12Terminal(terminal observation.Object) error {
	want := observation.Object{"state": "STOP", "nextIndex": 12, "noAutomaticRecovery": true, "planSHA256": stage12Plan, "reason": "STAGE_FAILED_OR_INTERRUPTED:MIXED_ROLLBACK_VERIFIED"}
	if observation.Digest(terminal) != observation.Digest(want) {
		return errors.New("only the exact stage-12 STOP is supported")
	}
	return nil
}
func stage12Owner(s Snapshot) error {
	if observation.At(rawIndex(s.Applications)[AppRef(Source).Key()], "metadata", "uid") != stage12OwnerUID {
		return errors.New("stage-12 source UID mismatch")
	}
	return nil
}
func (f *Stage12Incident) Plan(impl observation.Implementation) (Plan, error) {
	return PrepareContinuation(f.predecessor, impl)
}
func stage12AuditContinuity(frozen, live observation.Object) error {
	known, current := allAuditIDs(frozen), allAuditIDs(live)
	for id := range known {
		if id == "" || !current[id] {
			return errors.New("stage-12 audit history lost")
		}
	}
	for _, v := range observation.Slice(live["events"]) {
		e := observation.Map(v)
		if !known[observation.String(e["auditID"])] && strings.HasPrefix(observation.String(e["userAgent"]), "kubectl/") {
			return errors.New("operator write after stage-12 STOP")
		}
	}
	return nil
}
func (x *Executor) stage12Fence(ctx context.Context, f *Stage12Incident) error {
	if e := x.fence(ctx, stage12Git); e != nil {
		return e
	}
	lock, e := observation.RegularPrivate(filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock"))
	if e != nil || !bytesEqual(lock, f.lock) || observation.SHA(lock) != stage12Lock {
		return errors.New("active stage-12 STOP lock changed")
	}
	if e = x.auditScope(); e != nil {
		return e
	}
	live, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return e
	}
	return stage12AuditContinuity(f.audit, live)
}

// Stage12Anchor creates fresh read-only facts, never a historical checkpoint.
func (x *Executor) Stage12Anchor(ctx context.Context, f *Stage12Incident, dir string) (Snapshot, error) {
	var empty Snapshot
	if e := f.predecessor.CheckPlan(x.Plan); e != nil {
		return empty, e
	}
	delegate := x.Runner
	guard := &ReadOnlyRunner{Delegate: delegate}
	x.Runner = guard
	defer func() { x.Runner = delegate }()
	if e := x.preflightAt(ctx, stage12Git); e != nil {
		return empty, e
	}
	x.baseline = &f.predecessor.Baseline
	x.baselineAudit = allAuditIDs(f.predecessor.audit)
	if e := x.stage12Fence(ctx, f); e != nil {
		return empty, e
	}
	s, e := Capture(ctx, x.Reader, x.Plan, 12, x.baseline)
	if e != nil {
		return s, e
	}
	if e = observation.CreatePrivate(filepath.Join(dir, "opening-snapshot.json"), observation.Bytes(s)); e != nil {
		return s, e
	}
	if a := Assess(x.Plan, 12, s, x.baseline, &f.previous, x.Desired, nil); a.Ownership != "VERIFIED" {
		return s, fmt.Errorf("stage-12 anchor: %v", a.Reasons)
	}
	if e = stage12Owner(s); e != nil {
		return s, e
	}
	latest, gate, e := x.AtlasGate(ctx, 12, s, x.baseline, dir)
	if se := observation.CreatePrivate(filepath.Join(dir, "stage12-anchor-snapshot.json"), observation.Bytes(latest)); se != nil {
		return latest, se
	}
	if e != nil {
		return latest, e
	}
	assessment := Assess(x.Plan, 12, latest, x.baseline, &f.previous, x.Desired, gate)
	if !assessment.Passed() || guard.Denied != 0 {
		return latest, errors.New("stage-12 full Gate-B not verified")
	}
	if e = x.stage12Fence(ctx, f); e != nil {
		return latest, e
	}
	receipt := observation.Object{"purpose": "fresh stage-12 anchor; historical STOP unchanged", "planSHA256": observation.Digest(x.Plan), "predecessorManifestSHA256": Stage12Manifest, "predecessorPlanSHA256": stage12Plan, "predecessorLockSHA256": stage12Lock, "firstIndex": 13, "snapshotSHA256": observation.Digest(latest), "assessment": assessment, "gate": gate}
	if e = observation.CreatePrivate(filepath.Join(dir, "stage12-anchor.json"), observation.Bytes(receipt)); e != nil {
		return latest, e
	}
	return latest, nil
}
func stage12Attempt(plan Plan, dir string, baseline *Snapshot) (*Attempt, error) {
	step := Steps(plan)[13]
	if step.Stage != "SECOND_SOURCE_RELEASED" || step.Owner != "" || step.Create || step.Sync || step.PublishRevision != plan.Revisions["detached-forward"].Commit || len(step.Release) != 1 || step.Release[0] != Source {
		return nil, errors.New("stage-12 successor must be original second source release")
	}
	a, e := NewAttempt(plan, dir)
	if e != nil {
		return nil, e
	}
	a.next = 13
	a.baseline = baseline
	return a, nil
}
func (x *Executor) ExecuteStage12(ctx context.Context, approved string, f *Stage12Incident) error {
	if approved != observation.Digest(x.Plan) {
		return errors.New("exact stage-12 plan approval required")
	}
	a, e := stage12Attempt(x.Plan, x.EvidenceDirectory, &f.predecessor.Baseline)
	if e != nil {
		return e
	}
	anchor, e := x.Stage12Anchor(ctx, f, filepath.Join(x.EvidenceDirectory, "anchor"))
	if e != nil {
		_ = a.Stop("STAGE12_ANCHOR_REJECTED")
		return e
	}
	a.previous = &anchor
	lockPath := filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock")
	successor := observation.Bytes(observation.Object{"planSHA256": approved, "attempt": x.EvidenceDirectory, "predecessorLockSHA256": stage12Lock, "predecessorManifestSHA256": Stage12Manifest, "firstIndex": 13})
	if e = handoffLock(lockPath, f.lock, successor, x.EvidenceDirectory); e != nil {
		_ = a.Stop("LOCK_HANDOFF_FAILED")
		return e
	}
	e = runStages(ctx, a, x.Desired, x, x.baseline, &anchor, 13)
	if e == nil {
		lock, err := observation.RegularPrivate(lockPath)
		if err != nil || !bytesEqual(lock, successor) {
			return errors.New("completed stage-12 successor lock changed")
		}
		e = os.Remove(lockPath)
	}
	return e
}
