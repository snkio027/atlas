package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// This is a single reviewed recovery boundary, not an arbitrary stage resume.
const SourceReleasedManifest = "b48895b8a871c0d685b66a7bfc5fe829f7f7408d8e954542d8c2fa06bff1eb74"
const SourceReleasedPlan = "b195dc63e4ab2910bf14c2f351d7da6d7cfac91d9363ba08ecc1ff038f3fba70"

type ContinuationBinding struct {
	Schema               string `json:"schema"`
	ManifestSHA256       string `json:"manifestSHA256"`
	PlanSHA256           string `json:"planSHA256"`
	TerminalSHA256       string `json:"terminalSHA256"`
	CheckpointSHA256     string `json:"checkpointSHA256"`
	BaselineSHA256       string `json:"baselineSHA256"`
	LockSHA256           string `json:"lockSHA256"`
	AnchorSnapshotSHA256 string `json:"anchorSnapshotSHA256"`
	AnchorProofSHA256    string `json:"anchorProofSHA256"`
	NextIndex            int    `json:"nextIndex"`
}

func (b ContinuationBinding) Validate() error {
	if b.Schema != "ot1.source-released-continuation/v1" || b.ManifestSHA256 != SourceReleasedManifest || b.PlanSHA256 != SourceReleasedPlan || b.NextIndex != 2 {
		return errors.New("unsupported continuation boundary")
	}
	for _, h := range []string{b.TerminalSHA256, b.CheckpointSHA256, b.BaselineSHA256, b.LockSHA256, b.AnchorSnapshotSHA256, b.AnchorProofSHA256} {
		if !observation.Hash(h) {
			return errors.New("continuation binding incomplete")
		}
	}
	return nil
}

type bundleManifest struct {
	PredecessorManifestSHA256 string `json:"predecessorManifestSHA256,omitempty"`
	Schema                    int    `json:"schema"`
	PlanSHA256                string `json:"planSHA256"`
	Files                     map[string]struct {
		SHA256 string `json:"sha256"`
		Bytes  int64  `json:"bytes"`
	} `json:"files"`
}

func verifyBundle(dir, hash string) (bundleManifest, error) {
	var m bundleManifest
	b, e := observation.RegularPrivate(filepath.Join(dir, "EVIDENCE-MANIFEST.json"))
	if e != nil {
		return m, e
	}
	if observation.SHA(b) != hash || observation.Decode(b, &m, true) != nil || m.Schema != 1 || len(m.Files) == 0 {
		return m, errors.New("predecessor manifest mismatch")
	}
	for name, f := range m.Files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || name == "EVIDENCE-MANIFEST.json" {
			return m, errors.New("invalid authority bundle path")
		}
		data, e := bundledFile(dir, name)
		if e != nil || int64(len(data)) != f.Bytes || observation.SHA(data) != f.SHA256 {
			return m, fmt.Errorf("authority evidence changed: %s", name)
		}
	}
	e = filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in authority bundle")
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(dir, file)
		if err != nil {
			return err
		}
		if name == "EVIDENCE-MANIFEST.json" {
			return nil
		}
		if _, ok := m.Files[name]; !ok {
			return errors.New("unlisted authority bundle file")
		}
		return nil
	})
	return m, e
}

// Historic bundles contain executable/source files with their original modes.
// Require the enclosing archive to be private, all paths regular and hash-bound;
// do not chmod immutable history merely to satisfy a per-file 0600 convention.
func bundledFile(dir, name string) ([]byte, error) {
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("authority archive must be private")
	}
	current := dir
	for _, part := range strings.Split(name, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, e = os.Lstat(current)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symlink in archive")
		}
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("nonregular authority evidence")
	}
	return os.ReadFile(current)
}

type Predecessor struct {
	Plan             Plan
	Baseline, Anchor Snapshot
	Binding          ContinuationBinding
	Directory        string
	Lock             []byte
	audit            observation.Object
}

func snapshotProof(s Snapshot) (string, error) {
	facts := map[string]string{}
	for _, group := range [][]observation.Object{s.Envelope.Raw, s.Projects, s.Nodes} {
		for _, o := range group {
			ref := observation.Reference(o)
			if facts[ref.Key()] != "" {
				return "", errors.New("duplicate anchor identity")
			}
			proof, e := observation.Proof(o, readiness(ref))
			if e != nil {
				return "", e
			}
			facts[ref.Key()] = proof
		}
	}
	return observation.Digest(facts), nil
}
func LoadPredecessor(ctx context.Context, root, repo, dir, manifest string) (*Predecessor, error) {
	if manifest != SourceReleasedManifest {
		return nil, errors.New("only the reviewed SOURCE_RELEASED bundle is supported")
	}
	m, e := verifyBundle(dir, manifest)
	if e != nil {
		return nil, e
	}
	if m.PlanSHA256 != SourceReleasedPlan {
		return nil, errors.New("unsupported predecessor plan")
	}
	p, e := ReadPlan(ctx, root, repo, filepath.Join(dir, "plan.json"))
	if e != nil {
		return nil, e
	}
	if observation.Digest(p) != SourceReleasedPlan || p.Continuation != nil || p.EvidenceModel != "" {
		return nil, errors.New("predecessor implementation contract mismatch")
	}
	read := func(name string, dst any) error {
		if _, ok := m.Files[name]; !ok {
			return errors.New("required predecessor file not in manifest")
		}
		b, e := observation.RegularPrivate(filepath.Join(dir, name))
		if e != nil {
			return e
		}
		return observation.Decode(b, dst, true)
	}
	var terminal observation.Object
	if e = read("attempt/terminal.json", &terminal); e != nil {
		return nil, e
	}
	want := observation.Object{"state": "STOP", "nextIndex": 1, "noAutomaticRecovery": true, "planSHA256": SourceReleasedPlan, "reason": "STAGE_FAILED_OR_INTERRUPTED:SOURCE_RELEASED"}
	if observation.Digest(terminal) != observation.Digest(want) {
		return nil, errors.New("predecessor not stopped at source release")
	}
	base, e := ReadSnapshot(filepath.Join(dir, "attempt/00-BASELINE_ADOPTED-snapshot.json"))
	if e != nil {
		return nil, e
	}
	anchor, e := ReadSnapshot(filepath.Join(dir, "post-stop/source-released-snapshot.json"))
	if e != nil {
		return nil, e
	}
	desired, e := DesiredObjects(ctx, repo, p)
	if e != nil {
		return nil, e
	}
	gate, e := LoadGateEvidence(filepath.Join(dir, "attempt"), "00-BASELINE_ADOPTED", p, p.Phases[0], base)
	if e != nil {
		return nil, e
	}
	if !Assess(p, 0, base, nil, nil, desired, gate).Passed() || Assess(p, 1, anchor, &base, &base, desired, nil).Ownership != "VERIFIED" {
		return nil, errors.New("predecessor baseline or post-STOP anchor not proven")
	}
	var cp Checkpoint
	if e = read("attempt/00-BASELINE_ADOPTED-checkpoint.json", &cp); e != nil {
		return nil, e
	}
	if cp.Index != 0 || cp.PlanSHA256 != SourceReleasedPlan || cp.SnapshotSHA256 != observation.Digest(base) || !cp.Assessment.Passed() {
		return nil, errors.New("predecessor checkpoint mismatch")
	}
	lock, e := observation.RegularPrivate(filepath.Join(dir, "stopped-run.lock.json"))
	if e != nil {
		return nil, e
	}
	var lockData observation.Object
	if observation.Decode(lock, &lockData, true) != nil || lockData["planSHA256"] != SourceReleasedPlan {
		return nil, errors.New("predecessor STOP lock mismatch")
	}
	// The manifest pins all four intents/results and the original executable.
	// Metadata-only audit cannot reconstruct a DELETE payload from its input hash.
	// We retain that limitation; the pinned bounded executable supplies Orphan/UID/RV intent.
	if m.Files["authority-executable/atlas-ot1"].SHA256 != p.Implementation.BinarySHA256 {
		return nil, errors.New("original executable provenance missing")
	}
	requests := 0
	for name := range m.Files {
		if strings.HasPrefix(name, "attempt/request-") {
			requests++
		}
		if strings.HasPrefix(name, "attempt/") && strings.HasSuffix(name, "-checkpoint.json") && name != "attempt/00-BASELINE_ADOPTED-checkpoint.json" {
			return nil, errors.New("unexpected later checkpoint")
		}
	}
	if requests != 8 {
		return nil, errors.New("unexpected predecessor request set")
	}
	for i := 0; i < 4; i++ {
		var q, result observation.Object
		if e = read(fmt.Sprintf("attempt/request-%03d-intent.json", i), &q); e != nil {
			return nil, e
		}
		if e = read(fmt.Sprintf("attempt/request-%03d-result.json", i), &result); e != nil {
			return nil, e
		}
		if q["planSHA256"] != SourceReleasedPlan || q["stage"] != "SOURCE_RELEASED" || fmt.Sprint(result["exitCode"]) != "0" {
			return nil, errors.New("uncertain predecessor request outcome")
		}
		if i < 3 && q["tool"] != "git" || i == 3 && q["tool"] != "kubectl" {
			return nil, errors.New("unexpected predecessor request tool")
		}
	}
	var auditArtifact GateArtifact
	if e = read("attempt/00-BASELINE_ADOPTED-audit-after.json", &auditArtifact); e != nil {
		return nil, e
	}
	proof, e := snapshotProof(anchor)
	if e != nil {
		return nil, e
	}
	binding := ContinuationBinding{Schema: "ot1.source-released-continuation/v1", ManifestSHA256: manifest, PlanSHA256: SourceReleasedPlan, TerminalSHA256: m.Files["attempt/terminal.json"].SHA256, CheckpointSHA256: m.Files["attempt/00-BASELINE_ADOPTED-checkpoint.json"].SHA256, BaselineSHA256: m.Files["attempt/00-BASELINE_ADOPTED-snapshot.json"].SHA256, LockSHA256: observation.SHA(lock), AnchorSnapshotSHA256: m.Files["post-stop/source-released-snapshot.json"].SHA256, AnchorProofSHA256: proof, NextIndex: 2}
	if e = binding.Validate(); e != nil {
		return nil, e
	}
	return &Predecessor{Plan: p, Baseline: base, Anchor: anchor, Binding: binding, Directory: dir, Lock: lock, audit: auditArtifact.Data}, nil
}
func PrepareContinuation(old *Predecessor, impl observation.Implementation) (Plan, error) {
	if impl.Dirty || !observation.FullSHA(impl.Revision) || !observation.Hash(impl.BinarySHA256) {
		return Plan{}, errors.New("clean implementation identity required")
	}
	var p Plan
	if e := observation.Decode(observation.Bytes(old.Plan), &p, true); e != nil {
		return p, e
	}
	p.Implementation = impl
	p.EvidenceModel = EvidenceModel
	b := old.Binding
	p.Continuation = &b
	return p, nil
}
func (p *Predecessor) CheckPlan(plan Plan) error {
	expected, e := PrepareContinuation(p, plan.Implementation)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(expected, plan) {
		return errors.New("continuation changed predecessor scope, revisions or binding")
	}
	return nil
}

// ContinuationAnchor is read-only: no lock handoff, cluster write, Git checkout,
// refresh or resampling. A later continue command must repeat this fresh check.
func (x *Executor) ContinuationAnchor(ctx context.Context, old *Predecessor) (Snapshot, error) {
	var empty Snapshot
	if e := old.CheckPlan(x.Plan); e != nil {
		return empty, e
	}
	if e := x.preflightAt(ctx, x.Plan.Phases[1].Revision); e != nil {
		return empty, e
	}
	lock, e := observation.RegularPrivate(filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock"))
	if e != nil || observation.SHA(lock) != old.Binding.LockSHA256 {
		return empty, errors.New("active STOP lock changed")
	}
	x.baseline = &old.Baseline
	x.baselineAudit = allAuditIDs(old.audit)
	if e = x.auditScope(); e != nil {
		return empty, e
	}
	// No new operator writes are permitted after baseline except the one pinned
	// orphan DELETE. Controller reconciliation remains observable, not forbidden.
	live, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return empty, e
	}
	known := map[string]bool{}
	for _, id := range observation.Slice(old.audit["kubectlMutationAuditIDs"]) {
		known[observation.String(id)] = true
	}
	release := 0
	for _, raw := range observation.Slice(live["events"]) {
		event := observation.Map(raw)
		if !strings.HasPrefix(observation.String(event["userAgent"]), "kubectl/") || known[observation.String(event["auditID"])] {
			continue
		}
		if event["auditID"] != "924077d5-802f-456d-988e-110d2e155136" || event["verb"] != "delete" || observation.At(event, "objectRef", "name") != Source || observation.At(event, "objectRef", "resource") != "applications" || observation.At(event, "objectRef", "namespace") != "argocd" || fmt.Sprint(observation.At(event, "responseStatus", "code")) != "200" {
			return empty, errors.New("unreviewed operator write since baseline")
		}
		release++
	}
	if release != 1 {
		return empty, errors.New("source release audit unavailable")
	}
	s, e := Capture(ctx, x.Reader, x.Plan, 1, &old.Baseline)
	if e != nil {
		return s, e
	}
	assessed := Assess(x.Plan, 1, s, &old.Baseline, &old.Baseline, x.Desired, nil)
	if assessed.Ownership != "VERIFIED" {
		return s, fmt.Errorf("continuation anchor: %v", assessed.Reasons)
	}
	proof, e := snapshotProof(s)
	if e != nil || proof != old.Binding.AnchorProofSHA256 {
		return s, errors.New("current semantic anchor differs from reviewed post-STOP state")
	}
	if e = checkNodes(s.Nodes, &old.Baseline, x.Plan.Target.Cluster); e != nil {
		return s, e
	}
	if e = x.fence(ctx, x.Plan.Phases[1].Revision); e != nil {
		return s, e
	}
	lock, e = observation.RegularPrivate(filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock"))
	if e != nil || observation.SHA(lock) != old.Binding.LockSHA256 {
		return s, errors.New("STOP lock changed during anchor")
	}
	return s, nil
}

func anchorReceipt(plan Plan, s Snapshot) observation.Object {
	return observation.Object{"schema": "ot1.continuation-anchor/v1", "stage": "CONTINUATION_ANCHOR_SOURCE_RELEASED", "state": "VERIFIED", "planSHA256": observation.Digest(plan), "snapshotSHA256": observation.Digest(s), "predecessor": plan.Continuation, "nextIndex": 2, "oldTerminalUnchanged": true}
}
func SaveContinuationCheck(dir string, plan Plan, s Snapshot) error {
	if e := observation.CreatePrivate(filepath.Join(dir, "anchor-snapshot.json"), observation.Bytes(s)); e != nil {
		return e
	}
	return observation.CreatePrivate(filepath.Join(dir, "anchor.json"), observation.Bytes(anchorReceipt(plan, s)))
}
func (x *Executor) Continue(ctx context.Context, approved string, old *Predecessor) error {
	if approved != observation.Digest(x.Plan) {
		return errors.New("exact continuation plan approval required")
	}
	anchor, e := x.ContinuationAnchor(ctx, old)
	if e != nil {
		return e
	}
	attempt, e := NewAttempt(x.Plan, x.EvidenceDirectory)
	if e != nil {
		return e
	}
	attempt.next = 2
	attempt.baseline = &old.Baseline
	attempt.previous = &anchor
	if e = SaveContinuationCheck(x.EvidenceDirectory, x.Plan, anchor); e != nil {
		return e
	}
	lockPath := filepath.Join(x.RuntimeRepository, ".state/ot1-run.lock")
	successor := observation.Bytes(observation.Object{"planSHA256": approved, "attempt": x.EvidenceDirectory, "predecessorLockSHA256": old.Binding.LockSHA256, "predecessorManifestSHA256": old.Binding.ManifestSHA256})
	if e = handoffLock(lockPath, old.Lock, successor, x.EvidenceDirectory); e != nil {
		_ = attempt.Stop("LOCK_HANDOFF_FAILED")
		return e
	}
	e = runStages(ctx, attempt, x.Desired, x, &old.Baseline, &anchor, 2)
	if e == nil {
		current, err := observation.RegularPrivate(lockPath)
		if err != nil || !bytesEqual(current, successor) {
			return errors.New("completed run lock ownership changed")
		}
		e = os.Remove(lockPath)
	}
	return e
}
