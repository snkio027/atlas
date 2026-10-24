package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"strings"
)

// captureCoherent retries reads only. It shares the caller's phase deadline and
// never changes the baseline, cursor, requests or stored proof. Even a permitted
// race remains invalid evidence; only a later complete capture can be assessed.
func (x *Executor) captureCoherent(ctx context.Context, index int, baseline, previous *Snapshot) (Snapshot, error) {
	var out Snapshot
	var discarded *Snapshot
	err := x.wait(ctx, func() (bool, error) {
		if e := x.fence(ctx, x.Plan.Phases[index].Revision); e != nil {
			return false, e
		}
		s, e := Capture(ctx, x.Reader, x.Plan, index, baseline)
		out = s
		if e != nil {
			return false, e
		}
		if discarded != nil {
			if e = x.applicationReadAdvance(index, discarded.ClosingApplications, s.Applications, previous); e != nil {
				return false, e
			}
		}
		if s.InventoryError == "" && len(s.Envelope.Reasons) == 0 && validateSnapshotProof(s) == nil {
			ready := true
			for _, apps := range applicationViews(s) {
				ok, e := applicationProgress(x.Plan, index, apps, previous, x.activeUID)
				if e != nil {
					return false, e
				}
				ready = ready && ok
			}
			if ready {
				return true, nil
			}
			// F12 is checked on all reads: a coherent sample may still be
			// waiting for comparison. Check immutable facts before waiting.
			if e := convergenceSafety(x.Plan, index, s.Envelope.Raw, s.Projects, baseline, previous, x.Desired); e != nil {
				return false, e
			}
			return false, x.auditScope()
		}
		if e = x.captureRace(index, s, baseline, previous); e != nil {
			return false, e
		}
		if e = x.auditScope(); e != nil {
			return false, e
		}
		discarded = &s
		return false, nil
	})
	if err != nil && out.Schema != 0 {
		// Preserve the final rejected read at every entry point. Earlier safe
		// races are development samples, not another immutable attempt chain.
		file := filepath.Join(x.EvidenceDirectory, stageStem(index, x.Plan.Phases[index].Stage.Name)+"-failed-observation.json")
		if saveErr := observation.CreatePrivate(file, observation.Bytes(out)); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	return out, err
}

// captureRace is a safety decision to discard, never an acceptance projection.
// All successful reads remain present, and every difference must be explained.
// In particular an unavailable closing inventory cannot hide behind an earlier
// legal Application advance.
func (x *Executor) captureRace(index int, s Snapshot, baseline, previous *Snapshot) error {
	switch s.InventoryError {
	case "", "INVENTORY_CHANGED", "APPLICATION_CHANGED_DURING_CAPTURE":
	default:
		return errors.New("capture read unavailable or target changed")
	}
	for _, reason := range s.Envelope.Reasons {
		if !strings.HasPrefix(reason, "SNAPSHOT_CHANGED:") {
			return errors.New("capture closing read unavailable")
		}
	}
	if s.Envelope.ProofVersion != observation.ProofVersion || baseline == nil {
		return errors.New("capture has no verified semantic baseline")
	}
	// Validate both raw views against the immutable baseline. Comparing only
	// differences would miss a simultaneous, stable resource/permission drift.
	for i, raw := range [][]observation.Object{s.Envelope.Raw, s.Envelope.ClosingRaw} {
		projects := s.Projects
		if i == 1 {
			projects = s.ClosingProjects
		}
		if e := convergenceSafety(x.Plan, index, raw, projects, baseline, previous, x.Desired); e != nil {
			return e
		}
	}
	open, close := rawIndex(s.Envelope.Raw), rawIndex(s.Envelope.ClosingRaw)
	if len(open) != len(close) || len(close) != len(s.Envelope.ClosingRaw) {
		return errors.New("capture raw membership changed")
	}
	rawApps, closingApps := []observation.Object{}, []observation.Object{}
	for _, o := range s.Envelope.Raw {
		ref := observation.Reference(o)
		other := close[ref.Key()]
		if ref.Kind == "Application" && ref.APIVersion == "argoproj.io/v1alpha1" {
			rawApps = append(rawApps, o)
			closingApps = append(closingApps, other)
		} else if !observation.SameProof(o, other, readiness(ref)) {
			return errors.New("resource proof changed during capture")
		}
	}
	for _, pair := range []struct {
		kind observation.Ref
		a, b []observation.Object
	}{
		{observation.Ref{APIVersion: "argoproj.io/v1alpha1", Kind: "AppProject", Namespace: "argocd"}, s.Projects, s.ClosingProjects},
		{observation.Ref{APIVersion: "v1", Kind: "Node"}, s.Nodes, s.ClosingNodes},
	} {
		a, ae := observation.InventoryProof(pair.a, pair.kind)
		b, be := observation.InventoryProof(pair.b, pair.kind)
		if ae != nil || be != nil || a != b {
			return errors.New("permission or node proof changed during capture")
		}
	}
	for _, nodes := range [][]observation.Object{s.Nodes, s.ClosingNodes} {
		if e := checkNodes(nodes, baseline, x.Plan.Target.Cluster); e != nil {
			return e
		}
		base := rawIndex(baseline.Nodes)
		for _, node := range nodes {
			if observation.Digest(observation.Semantic(node)) != observation.Digest(observation.Semantic(base[observation.Reference(node).Key()])) {
				return errors.New("node content drift during capture")
			}
		}
	}
	views := [][]observation.Object{s.Applications, rawApps, closingApps, s.ClosingApplications}
	for i, apps := range views {
		if _, e := observation.InventoryProof(apps, AppRef("")); e != nil {
			return e
		}
		if _, e := applicationProgress(x.Plan, index, apps, previous, x.activeUID); e != nil {
			return e
		}
		if i > 0 {
			if e := x.applicationReadAdvance(index, views[i-1], apps, previous); e != nil {
				return e
			}
		}
	}
	return nil
}

// The only additional race currently proved is monotonic comparison of an
// unchanged ordinary leaf. Operation/status/permission/tracking differences
// remain strict; F12 timestamps are independently checked by readiness/Assess.
func (x *Executor) applicationReadAdvance(index int, before, after []observation.Object, previous *Snapshot) error {
	a, b := rawIndex(before), rawIndex(after)
	if len(a) != len(before) || len(b) != len(after) || len(a) != len(b) || len(a) != len(x.Plan.Phases[index].Applications) {
		return errors.New("Application membership changed during capture")
	}
	prior := map[string]observation.Object{}
	if previous != nil {
		prior = rawIndex(previous.Applications)
	}
	for _, want := range x.Plan.Phases[index].Applications {
		key := AppRef(want.Name).Key()
		old, next := a[key], b[key]
		if observation.SameProof(old, next, "identity-content") {
			continue
		}
		if !desiredEquivalent(x.Plan, index, want, old, prior[key]) || !desiredEquivalent(x.Plan, index, want, next, prior[key]) {
			return fmt.Errorf("unexplained Application change: %s", want.Name)
		}
		oldRevision := observation.String(observation.At(old, "status", "sync", "revision"))
		nextRevision := observation.String(observation.At(next, "status", "sync", "revision"))
		position := func(revision string) int {
			for i := 0; i <= index; i++ {
				if x.Plan.Phases[i].Revision == revision {
					return i
				}
			}
			return -1
		}
		if position(oldRevision) >= position(nextRevision) {
			return errors.New("Application comparison did not advance within the plan")
		}
		// Compare every remaining fact without altering the captured objects or
		// Proof. Unknown extra differences cannot be erased by a later good read.
		copy := observation.Clone(next)
		observation.Map(observation.At(copy, "status", "sync"))["revision"] = oldRevision
		if !observation.SameProof(old, copy, "identity-content") {
			return fmt.Errorf("additional Application drift: %s", want.Name)
		}
	}
	return nil
}

func applicationViews(s Snapshot) [][]observation.Object {
	views := [][]observation.Object{s.Applications}
	for _, raw := range [][]observation.Object{s.Envelope.Raw, s.Envelope.ClosingRaw} {
		apps := []observation.Object{}
		for _, o := range raw {
			if observation.Reference(o).APIVersion == "argoproj.io/v1alpha1" && observation.Reference(o).Kind == "Application" {
				apps = append(apps, o)
			}
		}
		views = append(views, apps)
	}
	return append(views, s.ClosingApplications)
}
