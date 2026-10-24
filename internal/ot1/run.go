package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"time"
)

// Driver is private to the bounded ceremony, never part of Observation. Each
// action must use fresh UID/RV/spec/source fences. A failed action is not retried.
type Driver interface {
	Transition(context.Context, int, *Snapshot) error
	Converge(context.Context, int, *Snapshot, *Snapshot) (Snapshot, error)
	AtlasGate(context.Context, int, Snapshot, *Snapshot, string) (Snapshot, *GateProof, error)
}

func Run(ctx context.Context, plan Plan, approvedPlan string, dir string, desired map[string]observation.Object, driver Driver) error {
	if plan.Continuation != nil {
		return errors.New("use explicit SOURCE_RELEASED continuation")
	}
	if approvedPlan != observation.Digest(plan) {
		return errors.New("exact bounded-plan SHA approval required")
	}
	attempt, e := NewAttempt(plan, dir)
	if e != nil {
		return e
	}
	return runStages(ctx, attempt, desired, driver, nil, nil, 0)
}
func runStages(ctx context.Context, attempt *Attempt, desired map[string]observation.Object, driver Driver, baseline, previous *Snapshot, start int) error {
	plan, dir := attempt.Plan, attempt.Directory
	for i := start; i < len(plan.Phases); i++ {
		phase := plan.Phases[i]
		stageCtx, cancel := context.WithTimeout(ctx, time.Duration(plan.MaxStageSeconds)*time.Second)
		err := func() error {
			if e := stageCtx.Err(); e != nil {
				return e
			}
			// Durable intent precedes any mutation. An interrupted request has no retry
			// path; a new run cannot reopen this attempt directory.
			intent := observation.Object{"planSHA256": observation.Digest(plan), "index": i, "step": Steps(plan)[i], "startedAt": time.Now().UTC()}
			if e := observation.CreatePrivate(dir+"/"+stageStem(i, phase.Stage.Name)+"-intent.json", observation.Bytes(intent)); e != nil {
				return e
			}
			if e := driver.Transition(stageCtx, i, previous); e != nil {
				return e
			}
			snapshot, e := driver.Converge(stageCtx, i, baseline, previous)
			if e != nil {
				return e
			}
			var gate *GateProof
			if phase.Stage.AtlasGate {
				snapshot, gate, e = driver.AtlasGate(stageCtx, i, snapshot, baseline, dir)
				if e != nil {
					return e
				}
			}
			if e := stageCtx.Err(); e != nil {
				return e
			}
			checkpoint, e := attempt.Record(snapshot, desired, gate)
			if e != nil {
				return e
			}
			if !checkpoint.Assessment.Passed() {
				return fmt.Errorf("stage %s did not pass", phase.Stage.Name)
			}
			copy := snapshot
			if i == 0 {
				baseline = &copy
			}
			previous = &copy
			return nil
		}()
		cancel()
		if err != nil {
			// Record already writes STOP on an invalid checkpoint. Do not replace it.
			if !attempt.stopped {
				_ = attempt.Stop("STAGE_FAILED_OR_INTERRUPTED:" + phase.Stage.Name)
			}
			return err
		}
	}
	return nil
}
