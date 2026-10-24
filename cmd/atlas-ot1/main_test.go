package main

import (
	"context"
	"github.com/snkio027/atlas/internal/ot1"
	"testing"
	"time"
)

func TestCLIRejectsMutationAndIncompletePreparation(t *testing.T) {
	for _, args := range [][]string{{}, {"run"}, {"apply"}, {"delete"}, {"plan"}, {"capture"}, {"prepare-profile"}, {"prepare-action"}, {"inspect-plan", "extra"}} {
		code, e := run(context.Background(), args)
		if code != 2 || e == nil {
			t.Fatalf("unsafe/incomplete command accepted: %v", args)
		}
	}
}

func TestRunDeadlineCoversPlanAndPreservesCallerControl(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute)
	for _, tc := range []struct {
		phases int
		budget time.Duration
	}{{29, 160 * time.Minute}, {5, 40 * time.Minute}} {
		plan := ot1.Plan{Phases: make([]ot1.Phase, tc.phases), MaxStageSeconds: 300}
		ctx, cancel := runContext(context.Background(), started, plan)
		deadline, ok := ctx.Deadline()
		cancel()
		if !ok || !deadline.Equal(started.Add(tc.budget)) {
			t.Fatalf("plan budget truncated or preparation excluded: %v", deadline)
		}
	}
	parent, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	ctx, cancel := runContext(parent, started, ot1.Plan{Phases: make([]ot1.Phase, 29), MaxStageSeconds: 300})
	defer cancel()
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if !got.Equal(want) {
		t.Fatal("caller deadline discarded")
	}
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatal("caller cancellation discarded")
	}
}
