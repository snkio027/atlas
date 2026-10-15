package workloadrun

import (
	"atlas-refactor/internal/observation"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Reproduce r2's predecessor recheck using compiled trees, actual receipt/Gate
// evidence and the production read poll. The dependent effect is synthetic;
// these tests do not claim a real Git publication or cluster observation.
func TestPublicationRecheckPendingPreservesGateAndUIDHistory(t *testing.T) {
	c, phases := compiledRollout(t)
	for _, failure := range []string{"", "uid", "unavailable"} {
		t.Run(failure, func(t *testing.T) {
			w, p, prior := rolloutFixture(t, c, phases, "infrastructure")
			original := compiledGate(t, phases[prior.Phase], prior)
			if err := w.retainGate(p, original); err != nil {
				t.Fatal(err)
			}
			gatePath := filepath.Join(filepath.Dir(w.publicationPath(p, prior.Phase)), prior.Phase+"-gate.json")
			before, err := regular(gatePath, true)
			if err != nil {
				t.Fatal(err)
			}
			session, err := w.rolloutContract(context.Background(), phases[prior.Phase], prior.Commit)
			if err != nil || session.active {
				t.Fatal("predecessor recheck must retain closed Gate", err)
			}
			const id = "argoproj.io/Application/platform-argocd/foundation"
			uid := original.UID[id]
			if uid == "" {
				t.Fatal("fixture lacks foundation identity")
			}
			reads, effects := 0, 0
			unavailable := errors.New("API unavailable")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			report, err := w.waitObservation(ctx, original, 0, func(context.Context) (Observation, error) {
				reads++
				liveUID := uid
				if reads > 1 && failure == "uid" {
					liveUID = "replacement"
				}
				if err := session.pin(id, liveUID); err != nil {
					return original, err
				}
				if reads == 1 {
					return original, Pending("observation changed during capture: " + id)
				}
				if failure == "unavailable" {
					return original, unavailable
				}
				return original, nil
			})
			if err == nil {
				if err = w.retainGate(p, report); err != nil {
					t.Fatal(err)
				}
				// Models the non-retried dependent publication boundary, after
				// successful observation and durable predecessor evidence.
				effects++
			}
			wantEffects := 0
			if failure == "" {
				wantEffects = 1
			}
			if reads != 2 || effects != wantEffects || (err == nil) != (failure == "") {
				t.Fatal(reads, effects, err)
			}
			if failure == "unavailable" && !errors.Is(err, unavailable) {
				t.Fatal("fatal error lost", err)
			}
			after, e := regular(gatePath, true)
			if e != nil || string(before) != string(after) {
				t.Fatal("immutable prior Gate overwritten", e)
			}
			var latest Observation
			b, e := regular(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), true)
			if e != nil || observation.Decode(b, &latest, true) != nil || latest.Revision != prior.Commit || latest.Phase != prior.Phase {
				t.Fatal("latest read not retained", e)
			}
			wantState := gateReady
			if failure != "" {
				wantState = gateRejected
			}
			if latest.Gate == nil || latest.Gate.State != wantState {
				t.Fatal(latest.Gate)
			}
			if _, e = os.Stat(w.publicationPath(p, "consumer") + ".intent"); !os.IsNotExist(e) {
				t.Fatal("read wait produced a publication intent", e)
			}
		})
	}
}

func TestPublicationRecheckDeadlineAndCancellationStopBeforeEffects(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "canceled"}[canceled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, _ := sequenceFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				defer cancel()
				start := time.Now()
				reads, effects := 0, 0
				initial := Observation{Schema: 1, Phase: "infrastructure", Revision: strings.Repeat("a", 40), Runtime: "UNPROVEN"}
				_, err := w.waitObservation(ctx, initial, 5*time.Second, func(context.Context) (Observation, error) {
					reads++
					if canceled {
						cancel()
						return initial, nil // A late Ready cannot undo cancellation.
					}
					return initial, Pending("observation changed during capture")
				})
				if err == nil {
					effects++
				}
				wantErr, elapsed := context.DeadlineExceeded, 15*time.Minute
				if canceled {
					wantErr, elapsed = context.Canceled, 0
				}
				if !errors.Is(err, wantErr) || reads == 0 || canceled && reads != 1 || effects != 0 || time.Since(start) != elapsed {
					t.Fatal(err, reads, effects, time.Since(start))
				}
				b, e := regular(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), true)
				var latest Observation
				if e != nil || observation.Decode(b, &latest, true) != nil || latest.Gate == nil || latest.Gate.State != gateRejected {
					t.Fatal("terminal read failure not persisted", e)
				}
			})
		})
	}
}
