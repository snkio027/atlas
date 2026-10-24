//go:build ot1_stage12_continuation

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

func stage12Fixture(t *testing.T) (Plan, map[string]observation.Object, []Snapshot) {
	p, d := syntheticPlan(t)
	revision := strings.Repeat("e", 40)
	p.Revisions["detached-forward"] = Revision{Commit: revision}
	for i := 13; i < len(p.Phases); i++ {
		p.Phases[i].Revision = revision
		for j := range p.Phases[i].Applications {
			p.Phases[i].Applications[j].Revision = revision
		}
	}
	return p, d, syntheticSnapshots(t, p, d)
}
func TestStage12RunsOnlyRemaining16Stages(t *testing.T) {
	for _, failAt := range []int{-1, 13, 23, 28} {
		t.Run(fmt.Sprintf("stop_at_%d", failAt), func(t *testing.T) {
			p, d, ss := stage12Fixture(t)
			dir := filepath.Join(privateTemp(t), "attempt")
			a, e := stage12Attempt(p, dir, &ss[0])
			if e != nil {
				t.Fatal(e)
			}
			a.previous = &ss[12]
			driver := &simulatedDriver{t: t, plan: p, snapshots: ss, failAt: failAt}
			e = runStages(t.Context(), a, d, driver, &ss[0], &ss[12], 13)
			if (e == nil) != (failAt < 0) {
				t.Fatal(e)
			}
			count := 16
			if failAt >= 0 {
				count = failAt - 12
			}
			if len(driver.calls) != count || driver.calls[0] != 13 {
				t.Fatal("replayed/skipped/retried", driver.calls)
			}
			if _, e = os.Stat(filepath.Join(dir, "12-MIXED_ROLLBACK_VERIFIED-intent.json")); !os.IsNotExist(e) {
				t.Fatal("replayed stage 12")
			}
			if _, e = os.Stat(filepath.Join(dir, "12-MIXED_ROLLBACK_VERIFIED-checkpoint.json")); !os.IsNotExist(e) {
				t.Fatal("rewrote stage 12")
			}
		})
	}
}
func TestStage12RejectsTerminalOwnerAndAuditChanges(t *testing.T) {
	valid := observation.Object{"state": "STOP", "nextIndex": 12, "noAutomaticRecovery": true, "planSHA256": stage12Plan, "reason": "STAGE_FAILED_OR_INTERRUPTED:MIXED_ROLLBACK_VERIFIED"}
	if e := stage12Terminal(valid); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]any{"state": "VERIFIED", "nextIndex": 13, "noAutomaticRecovery": false, "planSHA256": "other", "reason": "different STOP"} {
		bad := observation.Clone(valid)
		bad[key] = value
		if stage12Terminal(bad) == nil {
			t.Fatal("accepted changed terminal", key)
		}
	}
	_, _, ss := stage12Fixture(t)
	app := rawIndex(ss[12].Applications)[AppRef(Source).Key()]
	observation.Map(app["metadata"])["uid"] = stage12OwnerUID
	if e := stage12Owner(ss[12]); e != nil {
		t.Fatal(e)
	}
	observation.Map(app["metadata"])["uid"] = "replacement"
	if stage12Owner(ss[12]) == nil {
		t.Fatal("accepted replacement")
	}
	frozen := observation.Object{"events": []any{observation.Object{"auditID": "old", "userAgent": "kubectl/v1"}}}
	if e := stage12AuditContinuity(frozen, frozen); e != nil {
		t.Fatal(e)
	}
	if stage12AuditContinuity(frozen, observation.Object{"events": []any{}}) == nil {
		t.Fatal("lost audit")
	}
	next := observation.Clone(frozen)
	next["events"] = append(observation.Slice(next["events"]), observation.Object{"auditID": "new", "userAgent": "kubectl/v1"})
	if stage12AuditContinuity(frozen, next) == nil {
		t.Fatal("operator write accepted")
	}
}

type stage12RejectRunner struct{ calls int }

func (r *stage12RejectRunner) Run(context.Context, atlas.Request) ([]byte, error) {
	r.calls++
	return nil, errors.New("injected preflight rejection")
}
func TestStage12FailedAnchorNeverHandsOffLock(t *testing.T) {
	p, d, ss := stage12Fixture(t)
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
	runner := &stage12RejectRunner{}
	x := &Executor{Plan: plan, RuntimeRepository: dir, EvidenceDirectory: filepath.Join(dir, "attempt"), Runner: runner, Desired: d}
	incident := &Stage12Incident{predecessor: old, previous: ss[11], lock: original}
	if x.ExecuteStage12(t.Context(), "unapproved", incident) == nil || runner.calls != 0 {
		t.Fatal("unapproved execution")
	}
	if x.ExecuteStage12(t.Context(), observation.Digest(plan), incident) == nil {
		t.Fatal("failed anchor accepted")
	}
	b, e := os.ReadFile(lock)
	if e != nil || !bytesEqual(b, original) {
		t.Fatal("STOP lock changed")
	}
	if _, e = os.Stat(lock + ".handoff"); !os.IsNotExist(e) {
		t.Fatal("handoff occurred before anchor")
	}
	if _, e = os.Stat(filepath.Join(x.EvidenceDirectory, "13-SECOND_SOURCE_RELEASED-intent.json")); !os.IsNotExist(e) {
		t.Fatal("mutation stage entered")
	}
	if runner.calls != 1 {
		t.Fatal("unexpected requests", runner.calls)
	}
}
