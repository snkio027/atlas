package workloadrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/workload"
	"os"
	"path/filepath"
	"time"
)

func (w *Workflow) wait(ctx context.Context, phase, revision string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	r, e := w.Compile(phase)
	if e != nil {
		return e
	}
	_, e = w.observeWait(ctx, r, revision, 10*time.Second)
	return e
}

// Deploy is a finite prerequisite-ordered publication procedure. It never resumes an incomplete
// externally mutated attempt or retries a failed write. A successful repeat
// verifies the final state without publishing, credential use or probe writes.
func (w *Workflow) Deploy(ctx context.Context, p Plan, approval string) (resultErr error) {
	if e := w.approve(p, approval); e != nil {
		return e
	}
	dir := filepath.Join(w.Config.StateDirectory, "authority", approval)
	complete, e := attemptComplete(dir)
	if e != nil {
		return e
	}
	if complete {
		if w.application() {
			if e := w.validateApplicationFinal(p); e != nil {
				return e
			}
		}
		b, e := regular(w.publicationPath(p, "consumer"), true)
		if e != nil {
			return e
		}
		var receipt Publication
		if e = workload.StrictDecode(b, &receipt); e != nil {
			return e
		}
		if e := w.wait(ctx, "consumer", receipt.Commit); e != nil {
			return e
		}
		if w.application() {
			return w.applicationReady(ctx)
		}
		return nil
	}
	if e := probeAvailable(dir); e != nil {
		return e
	}
	for _, phase := range p.Phases {
		for _, suffix := range []string{"", ".intent"} {
			if _, e := regular(w.publicationPath(p, phase)+suffix, true); e == nil {
				return errors.New("partial publication already exists; no automatic resume")
			} else if !os.IsNotExist(e) {
				return e
			}
		}
	}
	// Discover unavailable publication authority before image writes or private
	// credential generation, not at the first push after those preparations.
	if e := w.checkPublicationAccess(ctx); e != nil {
		return e
	}
	if e := w.CaptureBaseline(ctx, p, approval); e != nil {
		return e
	}
	// Replace ephemeral development output before this attempt so a failure in
	// preparation cannot accidentally archive an unrelated older observation.
	initial := Observation{Schema: 1, Phase: "preparation", ClusterUID: p.ClusterUID, Revision: p.Parent, Project: "UNKNOWN", Workload: "UNKNOWN", Binding: "UNKNOWN", Runtime: "UNPROVEN"}
	if e := save(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), workload.JSON(initial), false); e != nil {
		return e
	}
	external := false
	defer func() {
		if resultErr != nil && external {
			resultErr = w.retainStop(approval, resultErr)
		}
	}()
	external = true // Credential evidence and node image imports are retained too.
	if e := w.ImportImage(ctx, p, approval); e != nil {
		return e
	}
	if !w.application() || len(w.Model.Intent.Bindings) > 0 {
		if e := w.PrepareCredentials(ctx, p, approval); e != nil {
			return e
		}
	}
	for _, phase := range p.Phases {
		if w.Progress != nil {
			w.Progress("Publishing " + phase)
		}
		receipt, e := w.Publish(ctx, p, approval, phase)
		if e != nil {
			return e
		}
		if e = w.wait(ctx, phase, receipt.Commit); e != nil {
			return e
		}
		gate, e := regular(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), true)
		if e != nil {
			return e
		}
		if e = save(filepath.Join(dir, phase+"-gate.json"), gate, true); e != nil {
			return e
		}
	}
	// Metrics discovery may lag otherwise-complete reconciliation. Wait for it
	// before the single functional probe so retries do not repeat S3 writes.
	if e := w.waitMetrics(ctx); e != nil {
		return e
	}
	if w.application() {
		return w.finishApplication(ctx, p, approval)
	}
	return w.Probe(ctx, p, approval)
}
func (w *Workflow) waitMetrics(ctx context.Context) error {
	// The functional probe checks every target. This bounded read waits only for
	// Prometheus' first discovery, and never masks malformed/error responses.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return pollRead(ctx, 5*time.Second, w.readProgress(ctx), w.metrics)
}

// Only explicitly Waiting reads are retried. The caller owns a single deadline;
// cancellation is checked before every read. Mutations never enter this closure.
func pollRead(ctx context.Context, interval time.Duration, progress func(string), read func(context.Context) error) error {
	last := ""
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("S2 observation stopped (%s): %w", last, err)
		}
		err := read(ctx)
		// Cancellation cannot be converted into a successful Gate by a late read.
		if stopped := ctx.Err(); stopped != nil {
			return fmt.Errorf("S2 observation stopped (%s): %w", last, stopped)
		}
		if err == nil {
			return nil
		}
		var pending Pending
		if !errors.As(err, &pending) {
			return err
		}
		if string(pending) != last && progress != nil {
			progress(string(pending))
		}
		last = string(pending)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("S2 observation stopped (%s): %w", last, ctx.Err())
		case <-timer.C:
		}
	}
}

// ObserveFor uses one contract and UID history for the entire CLI wait, just as
// Deploy does. Repeated independent Observe calls would lose that history.
func (w *Workflow) ObserveFor(ctx context.Context, result workload.Result, revision string, limit time.Duration) (Observation, error) {
	if limit < 0 || limit > 15*time.Minute {
		return Observation{}, errors.New("observation wait outside 0..15m")
	}
	if limit == 0 {
		return w.Observe(ctx, result, revision)
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return w.observeWait(ctx, result, revision, 5*time.Second)
}
func (w *Workflow) observeWait(ctx context.Context, result workload.Result, revision string, interval time.Duration) (Observation, error) {
	initial := w.observationReport(result, revision)
	session, contractErr := w.rolloutContract(ctx, result, revision)
	return w.waitObservation(ctx, initial, interval, func(ctx context.Context) (Observation, error) {
		if contractErr != nil {
			return initial, contractErr
		}
		return w.observe(ctx, result, revision, session)
	})
}

// The same bounded read protocol serves publication, CLI and probe boundaries.
// Its callback only observes; a functional probe must never enter this poll.
func (w *Workflow) waitObservation(ctx context.Context, initial Observation, interval time.Duration, read func(context.Context) (Observation, error)) (report Observation, resultErr error) {
	report = initial
	defer func() {
		if resultErr != nil {
			report.Gate = &GateDecision{gateRejected, []string{resultErr.Error()}}
			// Persist this failing read, never the preceding successful Gate.
			if err := save(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), workload.JSON(report), false); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	resultErr = pollRead(ctx, interval, w.readProgress(ctx), func(ctx context.Context) error {
		var err error
		report, err = read(ctx)
		if e := save(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), workload.JSON(report), false); e != nil {
			return e
		}
		return err
	})
	return report, resultErr
}

// Keep the existing read-only retry contract; add bounded-wait context only to
// the ordinary user interface. This never resets or extends a deadline.
func (w *Workflow) readProgress(ctx context.Context) func(string) {
	if !w.application() || w.Progress == nil {
		return w.Progress
	}
	started := time.Now()
	return func(reason string) {
		budget := ""
		if deadline, ok := ctx.Deadline(); ok {
			budget = fmt.Sprintf("; remaining %s", time.Until(deadline).Truncate(time.Second))
		}
		w.Progress(fmt.Sprintf("%s (read-only wait; elapsed %s%s)", reason, time.Since(started).Truncate(time.Second), budget))
	}
}
