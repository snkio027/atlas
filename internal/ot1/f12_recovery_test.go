//go:build ot1_f12_recovery

package ot1

import (
	"context"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"testing"
)

func TestF12StartsWithWindowClosureAndNeverReplaysAdoption(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	driver := &simulatedDriver{t: t, plan: plan, snapshots: snapshots, failAt: -1}
	a, e := f12Attempt(plan, filepath.Join(privateTemp(t), "attempt"), &snapshots[0], &snapshots[6])
	if e != nil {
		t.Fatal(e)
	}
	if e = runStages(context.Background(), a, desired, driver, &snapshots[0], &snapshots[6], 7); e != nil {
		t.Fatal(e)
	}
	if len(driver.calls) != 22 || driver.calls[0] != 7 || driver.calls[21] != 28 {
		t.Fatal(driver.calls)
	}
}
func TestF12RejectsChangedOwnerOperationAndAudit(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	s := snapshots[6]
	app := rawIndex(s.Applications)[AppRef("observability-foundation").Key()]
	observation.Map(app["metadata"])["uid"] = f12OwnerUID
	operation := observation.Clone(observation.Map(observation.At(app, "status", "operationState")))
	if e := f12OwnerAnchor(s, operation); e != nil {
		t.Fatal(e)
	}
	observation.Map(app["metadata"])["uid"] = "replacement"
	if e := f12OwnerAnchor(s, operation); e == nil {
		t.Fatal("accepted replacement")
	}
	observation.Map(app["metadata"])["uid"] = f12OwnerUID
	observation.Map(observation.At(app, "status", "operationState"))["finishedAt"] = "2026-09-27T01:00:00Z"
	if e := f12OwnerAnchor(s, operation); e == nil {
		t.Fatal("accepted new operation")
	}
	frozen := observation.Object{"events": []any{observation.Object{"auditID": "original", "userAgent": "kubectl/v1"}}}
	if e := f12AuditContinuity(frozen, frozen); e != nil {
		t.Fatal(e)
	}
	if e := f12AuditContinuity(frozen, observation.Object{"events": []any{}}); e == nil {
		t.Fatal("accepted lost audit")
	}
	live := observation.Clone(frozen)
	live["events"] = append(observation.Slice(live["events"]), observation.Object{"auditID": "new", "userAgent": "kubectl/v1"})
	if e := f12AuditContinuity(frozen, live); e == nil {
		t.Fatal("accepted new operator write")
	}
}
