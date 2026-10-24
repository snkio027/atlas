package workloadrun

import (
	"context"
	"errors"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func probeReport(uid string) Observation {
	return Observation{Schema: 1, Phase: "consumer", ClusterUID: "fixture-cluster", Revision: strings.Repeat("a", 40), Project: "VERIFIED", Workload: "VERIFIED", Binding: "VERIFIED", Runtime: "UNPROVEN", Gate: &GateDecision{State: gateReady}, UID: map[string]string{appIdentity("envoy-gateway"): uid}}
}

// Reproduce the protocol failure with real semantic proofs and the production
// poll/report writer. No cluster, credentials or network is involved. The
// functional callback stands for the indivisible, non-retryable probe pass.
func TestProbeClosingChangesRetryReadsNotFunctionalEffects(t *testing.T) {
	for _, counts := range [][2]int{{0, 0}, {2, 0}, {0, 3}, {2, 3}} {
		w, _ := sequenceFixture(t)
		want, app := gateApp("envoy-gateway")
		session := gateSession([]observation.ExpectedApplication{want}, false)
		session.active = false
		reads := [2]int{}
		passes, writes := 0, 0
		report := probeReport(want.Name + "-uid")
		read := func(ctx context.Context) (Observation, error) {
			window := passes
			passes++
			return w.waitObservation(ctx, report, 0, func(context.Context) (Observation, error) {
				reads[window]++
				_, decision := session.applications([]observation.ExpectedApplication{want}, map[string]Object{want.Name: app})
				if err := decision.err(); err != nil {
					return report, err
				}
				closing := observation.Clone(app)
				if reads[window] <= counts[window] {
					// An allowed planned comparison revision can change during collection;
					// unlike RV bookkeeping, this must force another complete observation.
					mapping(at(closing, "status", "sync"))["revision"] = strings.Repeat("b", 40)
				}
				if err := session.pin(appIdentity(want.Name), str(at(closing, "metadata", "uid"))); err != nil {
					return report, err
				}
				if !observation.SameProof(app, closing, "identity-content") {
					return report, Pending("observation changed during capture: " + appIdentity(want.Name))
				}
				return report, nil
			})
		}
		after, facts, err := probeWithEvidence(context.Background(), read, func(context.Context) (Object, error) {
			writes++
			if reads[0] != counts[0]+1 || reads[1] != 0 {
				t.Fatal("functional effects escaped read boundaries", reads)
			}
			return Object{"roundtrip": "PASS"}, nil
		})
		if err != nil || writes != 1 || passes != 2 || reads != [2]int{counts[0] + 1, counts[1] + 1} || facts["roundtrip"] != "PASS" || after.Runtime != "UNPROVEN" {
			t.Fatal(err, writes, passes, reads, after, facts)
		}
	}
}

func TestProbeObservationFailuresStopWithoutRepeatingEffects(t *testing.T) {
	for _, window := range []int{0, 1} {
		for _, failure := range []string{"uid", "spec", "revision", "ssa", "error-condition", "unavailable"} {
			t.Run(failure+string(rune('0'+window)), func(t *testing.T) {
				w, _ := sequenceFixture(t)
				want, app := gateApp("envoy-gateway")
				session := gateSession([]observation.ExpectedApplication{want}, false)
				session.active = false
				passes, writes := 0, 0
				reads := [2]int{}
				report := probeReport(want.Name + "-uid")
				read := func(ctx context.Context) (Observation, error) {
					current := passes
					passes++
					return w.waitObservation(ctx, report, 0, func(context.Context) (Observation, error) {
						reads[current]++
						if current == window && reads[current] == 1 {
							return report, Pending("capture changed")
						}
						live := observation.Clone(app)
						if current == window {
							switch failure {
							case "uid":
								mapping(live["metadata"])["uid"] = "replacement"
							case "spec":
								mapping(live["spec"])["project"] = "foreign"
							case "revision":
								mapping(at(live, "status", "sync"))["revision"] = strings.Repeat("f", 40)
							case "ssa":
								mapping(live["metadata"])["managedFields"] = []any{}
							case "error-condition":
								mapping(live["status"])["conditions"] = []any{Object{"type": "ComparisonError", "message": "fixture"}}
							case "unavailable":
								return report, errors.New("API unavailable")
							}
						}
						_, decision := session.applications([]observation.ExpectedApplication{want}, map[string]Object{want.Name: live})
						report.Gate = &decision
						return report, decision.err()
					})
				}
				_, facts, err := probeWithEvidence(context.Background(), read, func(context.Context) (Object, error) { writes++; return Object{}, nil })
				if err == nil || facts != nil || writes != window || reads[window] != 2 || passes != window+1 {
					t.Fatal(err, facts, writes, reads, passes)
				}
				b, e := regular(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), true)
				if e != nil {
					t.Fatal(e)
				}
				var saved Observation
				if e = observation.Decode(b, &saved, true); e != nil || saved.Phase != "consumer" || saved.Gate.State != gateRejected || saved.Runtime != "UNPROVEN" {
					t.Fatal(saved, e)
				}
			})
		}
	}
}

func TestProbeFunctionalFailureNeverRetriesEvenIfPending(t *testing.T) {
	for _, failure := range []error{Pending("provider not materialized"), errors.New("unknown HTTPS write result")} {
		reads, writes := 0, 0
		_, facts, err := probeWithEvidence(context.Background(), func(context.Context) (Observation, error) { reads++; return probeReport("uid"), nil }, func(context.Context) (Object, error) { writes++; return nil, failure })
		if !errors.Is(err, failure) || facts != nil || reads != 1 || writes != 1 {
			t.Fatal(err, facts, reads, writes)
		}
	}
}

func TestProbeDeadlineSpansBothReadsAndFunctionalPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, _ := sequenceFixture(t)
		start := time.Now()
		passes, writes := 0, 0
		var firstDeadline time.Time
		read := func(ctx context.Context) (Observation, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("unbounded probe")
			}
			if passes == 0 {
				firstDeadline = deadline
			} else if deadline != firstDeadline {
				t.Fatal("post-probe deadline reset")
			}
			current := passes
			passes++
			return w.waitObservation(ctx, probeReport("uid"), time.Minute, func(context.Context) (Observation, error) {
				if current == 0 && time.Since(start) >= 14*time.Minute {
					return probeReport("uid"), nil
				}
				return probeReport("uid"), Pending("capture changed")
			})
		}
		_, facts, err := probeWithEvidence(context.Background(), read, func(ctx context.Context) (Object, error) {
			writes++
			deadline, _ := ctx.Deadline()
			if deadline != firstDeadline {
				t.Fatal("functional pass reset deadline")
			}
			time.Sleep(30 * time.Second)
			return Object{}, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || facts != nil || writes != 1 || passes != 2 || time.Since(start) != 15*time.Minute {
			t.Fatal(err, facts, writes, passes, time.Since(start))
		}
	})
}

func TestProbeCancellationNeverAllowsLateSuccess(t *testing.T) {
	for _, where := range []string{"before", "pre-read", "functional", "post-read"} {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if where == "before" {
				cancel()
			}
			w, _ := sequenceFixture(t)
			reads, writes := 0, 0
			read := func(ctx context.Context) (Observation, error) {
				return w.waitObservation(ctx, probeReport("uid"), 0, func(context.Context) (Observation, error) {
					reads++
					if where == "pre-read" || where == "post-read" && reads == 2 {
						cancel()
					}
					return probeReport("uid"), nil
				})
			}
			_, facts, err := probeWithEvidence(ctx, read, func(context.Context) (Object, error) {
				writes++
				if where == "functional" {
					cancel()
				}
				return Object{}, nil
			})
			wantWrites := 0
			if where == "functional" || where == "post-read" {
				wantWrites = 1
			}
			if !errors.Is(err, context.Canceled) || facts != nil || writes != wantWrites {
				t.Fatal(err, facts, reads, writes)
			}
		})
	}
}

func TestProbeFinalIdentityComparisonRejectsInventoryChange(t *testing.T) {
	calls := 0
	_, facts, err := probeWithEvidence(context.Background(), func(context.Context) (Observation, error) {
		calls++
		r := probeReport("uid")
		if calls == 2 {
			r.UID["/Service/demo/new"] = "new-uid"
		}
		return r, nil
	}, func(context.Context) (Object, error) { return Object{}, nil })
	if err == nil || facts != nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatal(err, facts)
	}
}
