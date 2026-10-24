//go:build ot1_f12_recovery

package ot1

// This file is compiled only for the single F12 incident below. It is not part
// of atlas-ot1, and cannot select an entry stage or another predecessor.
import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snkio027/atlas/internal/observation"
)

const F12Manifest = "57a0e0f74aaa11220a0f37625da0f2b0035060b5faf6e66d6259355534a7a806"
const f12Plan = "3e8db44c2a6c56b52ff7268be628bb34089dfd7227fe996f5912b6e6cb92ed47"
const f12Lock = "f3c6b273117d2104869a43ef3f017ebedcba5135657edc3e75d03c78c6c95f47"
const f12OwnerUID = "00afbd19-65db-4fba-8018-468dfd59d181"

// F12Incident retains the original SOURCE_RELEASED lineage and the later F12
// STOP separately. None of the historical checkpoints is rewritten.
type F12Incident struct {
	predecessor *Predecessor
	plan        Plan
	previous    Snapshot
	lock        []byte
	operation   observation.Object
	audit       observation.Object
}

func LoadF12(ctx context.Context, root, repo, sourceBundle, stopBundle string) (*F12Incident, error) {
	old, e := LoadPredecessor(ctx, root, repo, sourceBundle, SourceReleasedManifest)
	if e != nil {
		return nil, e
	}
	m, e := verifyBundle(stopBundle, F12Manifest)
	if e != nil {
		return nil, e
	}
	if m.PlanSHA256 != f12Plan || m.PredecessorManifestSHA256 != SourceReleasedManifest {
		return nil, errors.New("wrong F12 lineage")
	}
	plan, e := ReadPlan(ctx, root, repo, filepath.Join(stopBundle, "plan.json"))
	if e != nil {
		return nil, e
	}
	if observation.Digest(plan) != f12Plan || old.CheckPlan(plan) != nil || plan.Target.ClusterUID != "b886f730-ea3b-44b9-a904-8bd55ac345f2" || m.Files["atlas-ot1"].SHA256 != plan.Implementation.BinarySHA256 {
		return nil, errors.New("wrong F12 target or executable")
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
	want := observation.Object{"state": "STOP", "nextIndex": 6, "noAutomaticRecovery": true, "planSHA256": f12Plan, "reason": "STAGE_FAILED_OR_INTERRUPTED:MIXED_OBSERVABILITY_ADOPTED_WINDOW"}
	if observation.Digest(terminal) != observation.Digest(want) {
		return nil, errors.New("wrong F12 STOP")
	}
	previous, e := ReadSnapshot(filepath.Join(stopBundle, "attempt/05-MIXED_OBSERVABILITY_STRICT_REFUSED-snapshot.json"))
	if e != nil {
		return nil, e
	}
	var checkpoint Checkpoint
	if e = read("attempt/05-MIXED_OBSERVABILITY_STRICT_REFUSED-checkpoint.json", &checkpoint); e != nil {
		return nil, e
	}
	if checkpoint.Index != 5 || checkpoint.PlanSHA256 != f12Plan || checkpoint.SnapshotSHA256 != observation.Digest(previous) || !checkpoint.Assessment.Passed() {
		return nil, errors.New("F12 preceding checkpoint mismatch")
	}
	failed, e := ReadSnapshot(filepath.Join(stopBundle, "attempt/06-MIXED_OBSERVABILITY_ADOPTED_WINDOW-failed-observation.json"))
	if e != nil {
		return nil, e
	}
	app := rawIndex(failed.Applications)[AppRef("observability-foundation").Key()]
	if observation.String(observation.At(app, "metadata", "uid")) != f12OwnerUID || operationMatches(app, plan.Phases[6], plan.Phases[6].Stage.Name, "success") != nil || observation.At(app, "status", "operationState", "finishedAt") != "2026-09-27T14:56:57Z" {
		return nil, errors.New("F12 successful operation proof mismatch")
	}
	lock, e := bundledFile(stopBundle, "post-stop/active-stop-lock.json")
	if e != nil || observation.SHA(lock) != f12Lock {
		return nil, errors.New("F12 lock mismatch")
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
				return nil, errors.New("invalid F12 audit")
			}
			events = append(events, event)
		}
	}
	if e = scan.Err(); e != nil {
		return nil, e
	}
	if len(events) == 0 {
		return nil, errors.New("empty F12 audit")
	}
	return &F12Incident{old, plan, previous, lock, observation.Map(observation.At(app, "status", "operationState")), observation.Object{"events": events}}, nil
}

func (f *F12Incident) Plan(impl observation.Implementation) (Plan, error) {
	return PrepareContinuation(f.predecessor, impl)
}

// F12Anchor is a new coherent read at the completed adoption state, not a
// retroactive checkpoint for the failed run. Its only successor is index 7.
func (x *Executor) F12Anchor(ctx context.Context, f *F12Incident) (Snapshot, error) {
	var empty Snapshot
	if e := f.predecessor.CheckPlan(x.Plan); e != nil {
		return empty, e
	}
	if e := x.preflightAt(ctx, f.plan.Phases[6].Revision); e != nil {
		return empty, e
	}
	lockPath := filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock")
	lock, e := observation.RegularPrivate(lockPath)
	if e != nil || !bytesEqual(lock, f.lock) {
		return empty, errors.New("F12 active STOP lock changed")
	}
	x.baseline = &f.predecessor.Baseline
	// Check every original baseline event and all scoped writes since then.
	x.baselineAudit = allAuditIDs(f.predecessor.audit)
	if e = x.auditScope(); e != nil {
		return empty, e
	}
	live, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return empty, e
	}
	if e = f12AuditContinuity(f.audit, live); e != nil {
		return empty, e
	}
	s, e := Capture(ctx, x.Reader, x.Plan, 6, x.baseline)
	if e != nil {
		return s, e
	}
	if a := Assess(x.Plan, 6, s, x.baseline, &f.previous, x.Desired, nil); a.Ownership != "VERIFIED" {
		return s, fmt.Errorf("F12 anchor: %v", a.Reasons)
	}
	if e = f12OwnerAnchor(s, f.operation); e != nil {
		return s, e
	}
	if e = checkNodes(s.Nodes, x.baseline, x.Plan.Target.Cluster); e != nil {
		return s, e
	}
	if e = x.fence(ctx, x.Plan.Phases[6].Revision); e != nil {
		return s, e
	}
	// Repeat audit/lock fences after the coherent read, before any handoff.
	live, e = readAudit(x.RuntimeRepository)
	if e != nil {
		return s, e
	}
	if e = f12AuditContinuity(f.audit, live); e != nil {
		return s, e
	}
	lock, e = observation.RegularPrivate(lockPath)
	if e != nil || !bytesEqual(lock, f.lock) {
		return s, errors.New("F12 lock changed during anchor")
	}
	return s, nil
}
func f12OwnerAnchor(s Snapshot, operation observation.Object) error {
	app := rawIndex(s.Applications)[AppRef("observability-foundation").Key()]
	if observation.At(app, "metadata", "uid") != f12OwnerUID || observation.Digest(observation.At(app, "status", "operationState")) != observation.Digest(operation) {
		return errors.New("F12 owner identity/operation changed")
	}
	return nil
}
func f12AuditContinuity(frozen, live observation.Object) error {
	known, current := allAuditIDs(frozen), allAuditIDs(live)
	for id := range known {
		if id == "" || !current[id] {
			return errors.New("F12 audit history lost")
		}
	}
	for _, v := range observation.Slice(live["events"]) {
		e := observation.Map(v)
		if !known[observation.String(e["auditID"])] && strings.HasPrefix(observation.String(e["userAgent"]), "kubectl/") {
			return errors.New("operator write after F12 STOP")
		}
	}
	return nil
}
func f12Attempt(plan Plan, dir string, baseline, anchor *Snapshot) (*Attempt, error) {
	step := Steps(plan)[7]
	if step.Stage != "MIXED_OBSERVABILITY_STRICT_RESTORED" || step.Owner != "observability-foundation" || step.Mode != "strict" || step.Create || step.PublishRevision != "" || len(step.Release) != 0 || !step.Sync {
		return nil, errors.New("F12 first action must close observability window")
	}
	a, e := NewAttempt(plan, dir)
	if e != nil {
		return nil, e
	}
	a.next = 7
	a.baseline = baseline
	a.previous = anchor
	if e = observation.CreatePrivate(filepath.Join(dir, "f12-anchor-snapshot.json"), observation.Bytes(anchor)); e != nil {
		return nil, e
	}
	return a, nil
}

func (x *Executor) ExecuteF12(ctx context.Context, approved string, f *F12Incident) error {
	if approved != observation.Digest(x.Plan) {
		return errors.New("exact F12 plan approval required")
	}
	anchor, e := x.F12Anchor(ctx, f)
	if e != nil {
		return e
	}
	a, e := f12Attempt(x.Plan, x.EvidenceDirectory, x.baseline, &anchor)
	if e != nil {
		return e
	}
	lockPath := filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock")
	successor := observation.Bytes(observation.Object{"planSHA256": approved, "attempt": x.EvidenceDirectory, "predecessorLockSHA256": f12Lock, "predecessorManifestSHA256": F12Manifest, "firstIndex": 7})
	if e = handoffLock(lockPath, f.lock, successor, x.EvidenceDirectory); e != nil {
		_ = a.Stop("LOCK_HANDOFF_FAILED")
		return e
	}
	e = runStages(ctx, a, x.Desired, x, x.baseline, &anchor, 7)
	if e == nil {
		lock, err := observation.RegularPrivate(lockPath)
		if err != nil || !bytesEqual(lock, successor) {
			return errors.New("F12 completed lock ownership changed")
		}
		e = os.Remove(lockPath)
	}
	return e
}
