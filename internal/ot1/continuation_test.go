package ot1

import (
	"context"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"testing"
)

func TestLockHandoffPreservesPredecessorAndRejectsConflict(t *testing.T) {
	for _, conflict := range []string{"", "changed", "guard", "symlink"} {
		t.Run(conflict, func(t *testing.T) {
			dir := privateTemp(t)
			lock := filepath.Join(dir, "run.lock")
			attempt := filepath.Join(dir, "attempt")
			old, next := []byte("old STOP"), []byte("new ACTIVE")
			if e := observation.CreatePrivate(lock, old); e != nil {
				t.Fatal(e)
			}
			switch conflict {
			case "changed":
				if e := os.WriteFile(lock, []byte("foreign"), 0600); e != nil {
					t.Fatal(e)
				}
			case "guard":
				if e := observation.CreatePrivate(lock+".handoff", []byte("busy")); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Rename(lock, lock+".original"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(lock+".original", lock); e != nil {
					t.Fatal(e)
				}
			}
			e := handoffLock(lock, old, next, attempt)
			if conflict == "" {
				if e != nil {
					t.Fatal(e)
				}
				saved, _ := os.ReadFile(filepath.Join(attempt, "predecessor-lock.json"))
				active, _ := os.ReadFile(lock)
				if !bytesEqual(saved, old) || !bytesEqual(active, next) {
					t.Fatal("handoff data missing")
				}
				if handoffLock(lock, old, next, attempt) == nil {
					t.Fatal("reentry accepted")
				}
			} else {
				if e == nil {
					t.Fatal("conflict accepted")
				}
				active, _ := os.ReadFile(lock)
				if bytesEqual(active, next) {
					t.Fatal("conflicting lock overwritten")
				}
			}
		})
	}
}
func TestAuthorityBundleTamperingAndTraversal(t *testing.T) {
	for _, name := range []string{"evidence.json", "../escape"} {
		dir := privateTemp(t)
		data := []byte("evidence")
		if e := observation.CreatePrivate(filepath.Join(dir, "evidence.json"), data); e != nil {
			t.Fatal(e)
		}
		m := observation.Object{"schema": 1, "planSHA256": SourceReleasedPlan, "files": observation.Object{name: observation.Object{"sha256": observation.SHA(data), "bytes": len(data)}}}
		b := observation.Bytes(m)
		if e := observation.CreatePrivate(filepath.Join(dir, "EVIDENCE-MANIFEST.json"), b); e != nil {
			t.Fatal(e)
		}
		_, e := verifyBundle(dir, observation.SHA(b))
		if (e == nil) != (name == "evidence.json") {
			t.Fatal("bundle path validation", e)
		}
		if name == "evidence.json" {
			if e = os.WriteFile(filepath.Join(dir, name), []byte("tampered"), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = verifyBundle(dir, observation.SHA(b)); e == nil {
				t.Fatal("tampered evidence accepted")
			}
		}
	}
}
func TestContinuationRunsOnlyRemaining27Stages(t *testing.T) {
	for _, failAt := range []int{-1, 2, 7} {
		p, d, ss := revisionFixture(t)
		dir := filepath.Join(privateTemp(t), "attempt")
		a, e := NewAttempt(p, dir)
		if e != nil {
			t.Fatal(e)
		}
		a.next = 2
		a.baseline = &ss[0]
		a.previous = &ss[1]
		driver := &simulatedDriver{t: t, plan: p, snapshots: ss, failAt: failAt}
		e = runStages(context.Background(), a, d, driver, &ss[0], &ss[1], 2)
		if (e == nil) != (failAt < 0) {
			t.Fatal(e)
		}
		count := 27
		if failAt >= 0 {
			count = failAt - 1
		}
		if len(driver.calls) != count || driver.calls[0] != 2 {
			t.Fatal("replayed/retried stages", driver.calls)
		}
		if _, e = os.Stat(filepath.Join(dir, "01-SOURCE_RELEASED-checkpoint.json")); !os.IsNotExist(e) {
			t.Fatal("forged original checkpoint")
		}
	}
}
func TestContinuationCannotUseNormalRunOrAlterPlan(t *testing.T) {
	p, d, _ := revisionFixture(t)
	old := &Predecessor{Plan: p}
	impl := p.Implementation
	next, e := PrepareContinuation(old, impl)
	if e != nil {
		t.Fatal(e)
	}
	if e = old.CheckPlan(next); e != nil {
		t.Fatal(e)
	}
	next.MaxStageSeconds = 600
	if old.CheckPlan(next) == nil {
		t.Fatal("budget change accepted")
	}
	next.MaxStageSeconds = 300
	next.Phases[1].Revision = "other"
	if old.CheckPlan(next) == nil {
		t.Fatal("desired revision change accepted")
	}
	if Run(context.Background(), next, observation.Digest(next), filepath.Join(privateTemp(t), "attempt"), d, nil) == nil {
		t.Fatal("continuation admitted through normal run")
	}
	if _, e := LoadPredecessor(context.Background(), "", "", "", "other"); e == nil {
		t.Fatal("unknown predecessor accepted")
	}
}
