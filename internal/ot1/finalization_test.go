//go:build ot1_finalization

package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func finalizationFixture(t *testing.T) (Plan, map[string]observation.Object, []Snapshot) {
	p, d := syntheticPlan(t)
	revision := strings.Repeat("e", 40)
	p.Revisions["detached-reverse"] = Revision{Commit: revision}
	for i := 24; i < len(p.Phases); i++ {
		p.Phases[i].Revision = revision
		for j := range p.Phases[i].Applications {
			p.Phases[i].Applications[j].Revision = revision
		}
	}
	return p, d, syntheticSnapshots(t, p, d)
}
func TestFinalizationRunsOnlyRemainingFiveStages(t *testing.T) {
	for _, failAt := range []int{-1, 24, 26, 28} {
		t.Run(fmt.Sprintf("stop_at_%d", failAt), func(t *testing.T) {
			p, d, ss := finalizationFixture(t)
			dir := filepath.Join(privateTemp(t), "attempt")
			a, e := finalizationAttempt(p, dir, &ss[0])
			if e != nil {
				t.Fatal(e)
			}
			a.previous = &ss[23]
			driver := &simulatedDriver{t: t, plan: p, snapshots: ss, failAt: failAt}
			e = runStages(t.Context(), a, d, driver, &ss[0], &ss[23], 24)
			if (e == nil) != (failAt < 0) {
				t.Fatal(e)
			}
			count := 5
			if failAt >= 0 {
				count = failAt - 23
			}
			if len(driver.calls) != count || driver.calls[0] != 24 {
				t.Fatal("replayed/skipped/retried", driver.calls)
			}
			if _, e = os.Stat(filepath.Join(dir, "23-FORWARD_VERIFIED-intent.json")); !os.IsNotExist(e) {
				t.Fatal("replayed stage 23")
			}
			if _, e = os.Stat(filepath.Join(dir, "23-FORWARD_VERIFIED-checkpoint.json")); !os.IsNotExist(e) {
				t.Fatal("rewrote stage 23")
			}
		})
	}
}
func TestFinalizationRejectsTerminalOwnerAndAuditChanges(t *testing.T) {
	valid := observation.Object{"state": "STOP", "nextIndex": 23, "noAutomaticRecovery": true, "planSHA256": finalizationPlan, "reason": "STAGE_FAILED_OR_INTERRUPTED:FORWARD_VERIFIED"}
	if e := finalizationTerminal(valid); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]any{"state": "VERIFIED", "nextIndex": 24, "noAutomaticRecovery": false, "planSHA256": "other", "reason": "different STOP"} {
		bad := observation.Clone(valid)
		bad[key] = value
		if finalizationTerminal(bad) == nil {
			t.Fatal("accepted changed terminal", key)
		}
	}
	_, _, ss := finalizationFixture(t)
	for name, uid := range map[string]string{"secrets-foundation": "64af31ad-4f72-4e7d-a915-9db9e440c825", "observability-foundation": "30aefabc-a93f-4b3d-8e84-4fa1250dafe3", "storage-foundation": "a0a975e4-ec73-4b75-b6e1-22704f1b75b9"} {
		app := rawIndex(ss[23].Applications)[AppRef(name).Key()]
		observation.Map(app["metadata"])["uid"] = uid
	}
	if e := finalizationOwner(ss[23]); e != nil {
		t.Fatal(e)
	}
	app := rawIndex(ss[23].Applications)[AppRef("storage-foundation").Key()]
	observation.Map(app["metadata"])["uid"] = "replacement"
	if finalizationOwner(ss[23]) == nil {
		t.Fatal("accepted replacement")
	}
	frozen := observation.Object{"events": []any{observation.Object{"auditID": "old", "userAgent": "kubectl/v1"}}}
	if e := finalizationAuditContinuity(frozen, frozen); e != nil {
		t.Fatal(e)
	}
	if finalizationAuditContinuity(frozen, observation.Object{"events": []any{}}) == nil {
		t.Fatal("lost audit")
	}
	next := observation.Clone(frozen)
	next["events"] = append(observation.Slice(next["events"]), observation.Object{"auditID": "new", "userAgent": "kubectl/v1"})
	if finalizationAuditContinuity(frozen, next) == nil {
		t.Fatal("operator write accepted")
	}
}

type finalizationRejectRunner struct{ calls int }

func (r *finalizationRejectRunner) Run(context.Context, atlas.Request) ([]byte, error) {
	r.calls++
	return nil, errors.New("injected preflight rejection")
}
func TestFinalizationFailedAnchorNeverHandsOffLock(t *testing.T) {
	p, d, ss := finalizationFixture(t)
	old := &Predecessor{Plan: p, Baseline: ss[0]}
	plan, e := PrepareContinuation(old, p.Implementation)
	if e != nil {
		t.Fatal(e)
	}
	dir := privateTemp(t)
	lock := filepath.Join(dir, ".state/ot1-run.lock")
	original := []byte("existing STOP")
	if e = observation.CreatePrivate(lock, original); e != nil {
		t.Fatal(e)
	}
	runner := &finalizationRejectRunner{}
	x := &Executor{Plan: plan, RuntimeRepository: dir, EvidenceDirectory: filepath.Join(dir, "attempt"), Runner: runner, Desired: d}
	incident := &FinalizationIncident{predecessor: old, previous: ss[22], lock: original}
	if x.ExecuteFinalization(t.Context(), "unapproved", incident) == nil || runner.calls != 0 {
		t.Fatal("unapproved execution")
	}
	if x.ExecuteFinalization(t.Context(), observation.Digest(plan), incident) == nil {
		t.Fatal("failed anchor accepted")
	}
	b, e := os.ReadFile(lock)
	if e != nil || !bytesEqual(b, original) {
		t.Fatal("STOP lock changed")
	}
	if _, e = os.Stat(lock + ".handoff"); !os.IsNotExist(e) {
		t.Fatal("handoff occurred before anchor")
	}
	if _, e = os.Stat(filepath.Join(x.EvidenceDirectory, "24-REVERSE_TARGETS_RELEASED-intent.json")); !os.IsNotExist(e) {
		t.Fatal("mutation stage entered")
	}
	if runner.calls != 1 {
		t.Fatal("unexpected requests", runner.calls)
	}
}
