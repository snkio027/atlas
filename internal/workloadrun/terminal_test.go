package workloadrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/snkio027/atlas/internal/platform"
	"github.com/snkio027/atlas/internal/workload"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func probeAttemptFixture(t *testing.T) (*Workflow, Publication, string) {
	t.Helper()
	w, p := sequenceFixture(t)
	w.BinarySHA256 = strings.Repeat("b", 64)
	receipt := Publication{1, workload.Digest(workload.JSON(p)), "consumer", p.Parent, strings.Repeat("c", 40), strings.Repeat("d", 64)}
	dir := filepath.Dir(w.publicationPath(p, "consumer"))
	if err := save(filepath.Join(dir, "consumer.json"), workload.JSON(receipt), true); err != nil {
		t.Fatal(err)
	}
	if err := save(filepath.Join(w.Config.StateDirectory, "provider-prepared.json"), []byte("{\"synthetic\":true}\n"), false); err != nil {
		t.Fatal(err)
	}
	return w, receipt, dir
}

func TestProbeAttemptCannotReplay(t *testing.T) {
	for _, marker := range []string{"final.json", "terminal.json", "probe-started.json", "empty-intent", "conflicting-terminal"} {
		t.Run(marker, func(t *testing.T) {
			w, receipt, dir := probeAttemptFixture(t)
			name, data := marker, []byte("{}\n")
			if marker == "empty-intent" {
				name, data = "probe-started.json", nil
			}
			if marker == "conflicting-terminal" {
				name = "terminal.json"
				if err := save(filepath.Join(dir, "final.json"), data, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := save(filepath.Join(dir, name), data, true); err != nil {
				t.Fatal(err)
			}
			reads, effects := 0, 0
			err := w.runProbe(context.Background(), receipt, workload.Inventory{}, func(context.Context) (Observation, error) { reads++; return probeReport("healthy"), nil }, func(context.Context) (Object, error) { effects++; return Object{}, nil })
			if err == nil || reads != 0 || effects != 0 {
				t.Fatal(err, reads, effects)
			}
		})
	}
}

func TestProbeIntentBeforeEffectsAndAcrossInvocations(t *testing.T) {
	for _, fail := range []bool{false, true} {
		w, receipt, dir := probeAttemptFixture(t)
		effects := 0
		failure := errors.New("unknown functional outcome")
		read := func(context.Context) (Observation, error) { return probeReport("uid"), nil }
		exercise := func(context.Context) (Object, error) {
			effects++
			b, err := regular(filepath.Join(dir, "probe-started.json"), true)
			if err != nil {
				t.Fatal("effect before persisted intent", err)
			}
			var intent Object
			if err = workload.StrictDecode(b, &intent); err != nil || intent["planSHA256"] != receipt.PlanSHA256 || intent["deploymentCommit"] != receipt.Commit || intent["compilerSHA256"] != w.BinarySHA256 {
				t.Fatal(intent, err)
			}
			if fail {
				return nil, failure
			}
			return Object{"roundtrip": "PASS"}, nil
		}
		err := w.runProbe(context.Background(), receipt, workload.Inventory{}, read, exercise)
		if fail && !errors.Is(err, failure) || !fail && err != nil {
			t.Fatal(err)
		}
		// A new Workflow represents a separate CLI invocation, with no in-memory latch.
		next := &Workflow{Config: w.Config, BinarySHA256: w.BinarySHA256}
		if err = next.runProbe(context.Background(), receipt, workload.Inventory{}, read, exercise); err == nil || effects != 1 {
			t.Fatal(err, effects)
		}
		_, err = regular(filepath.Join(dir, "final.json"), true)
		if fail && !os.IsNotExist(err) || !fail && err != nil {
			t.Fatal("wrong final state", err)
		}
		if !fail {
			prepared, _ := regular(filepath.Join(w.Config.StateDirectory, "provider-prepared.json"), true)
			committed, err := regular(filepath.Join(w.Config.StateDirectory, "provider.json"), true)
			if err != nil || !bytes.Equal(prepared, committed) {
				t.Fatal("provider not committed", err)
			}
		}
	}
}

func TestProbeRechecksTerminalAndIntentAtFunctionalBoundary(t *testing.T) {
	for _, marker := range []string{"final.json", "terminal.json", "probe-started.json", "unwritable-intent"} {
		t.Run(marker, func(t *testing.T) {
			w, receipt, dir := probeAttemptFixture(t)
			effects := 0
			err := w.runProbe(context.Background(), receipt, workload.Inventory{}, func(context.Context) (Observation, error) {
				// The first entry check passed. Inject a persistence obstacle before the
				// functional boundary; the read itself still reports a healthy cluster.
				if marker == "unwritable-intent" {
					if err := os.Chmod(dir, 0500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.Chmod(dir, 0700) })
					if os.Geteuid() == 0 {
						t.Skip("root bypasses the filesystem write denial")
					}
				} else if err := save(filepath.Join(dir, marker), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
				return probeReport("uid"), nil
			}, func(context.Context) (Object, error) { effects++; return Object{}, nil })
			if err == nil || effects != 0 {
				t.Fatal(err, effects)
			}
		})
	}
}

func TestProbeIntentExclusiveEvenForIdenticalBytes(t *testing.T) {
	_, _, dir := probeAttemptFixture(t)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if claimProbe(dir, []byte("identical")) == nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("intent acquired more than once", winners.Load())
	}
	if err := claimProbe(dir, []byte("identical")); err == nil {
		t.Fatal("idempotent evidence mistaken for execution permission")
	}
}

func TestProbeProviderFailureCannotCommitFinal(t *testing.T) {
	for _, fault := range []string{"prepared-read", "provider-save"} {
		t.Run(fault, func(t *testing.T) {
			w, receipt, dir := probeAttemptFixture(t)
			if fault == "prepared-read" {
				if err := os.Remove(filepath.Join(w.Config.StateDirectory, "provider-prepared.json")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Join(w.Config.StateDirectory, "provider.json"), 0700); err != nil {
				t.Fatal(err)
			}
			effects := 0
			read := func(context.Context) (Observation, error) { return probeReport("uid"), nil }
			exercise := func(context.Context) (Object, error) { effects++; return Object{"roundtrip": "PASS"}, nil }
			err := w.runProbe(context.Background(), receipt, workload.Inventory{}, read, exercise)
			if err == nil || effects != 1 {
				t.Fatal(err, effects)
			}
			if _, err := regular(filepath.Join(dir, "final.json"), true); !os.IsNotExist(err) {
				t.Fatal("provider failure left final", err)
			}
			if err := save(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), workload.JSON(probeReport("uid")), false); err != nil {
				t.Fatal(err)
			}
			if stopped := w.retainStop(receipt.PlanSHA256, err); !errors.Is(stopped, err) {
				t.Fatal("lost original failure", stopped)
			}
			if _, err := regular(filepath.Join(dir, "terminal.json"), true); err != nil {
				t.Fatal(err)
			}
			if err = w.runProbe(context.Background(), receipt, workload.Inventory{}, read, exercise); err == nil || effects != 1 {
				t.Fatal("replayed incomplete finalization", err, effects)
			}
		})
	}
}

func TestRetainStopReportsAllEvidenceFailures(t *testing.T) {
	original := errors.New("functional write outcome unknown")
	for _, faults := range []string{"none", "read", "snapshot", "terminal", "both"} {
		t.Run(faults, func(t *testing.T) {
			w, receipt, dir := probeAttemptFixture(t)
			if faults != "read" {
				if err := save(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), []byte("current"), false); err != nil {
					t.Fatal(err)
				}
			}
			if faults == "snapshot" || faults == "both" {
				if err := save(filepath.Join(dir, "stop-observation.json"), []byte("old"), true); err != nil {
					t.Fatal(err)
				}
			}
			if faults == "terminal" || faults == "both" {
				if err := os.Mkdir(filepath.Join(dir, "terminal.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := w.retainStop(receipt.PlanSHA256, original)
			if !errors.Is(err, original) {
				t.Fatal("lost original error", err)
			}
			wantObservation := faults == "read" || faults == "snapshot" || faults == "both"
			wantTerminal := faults == "terminal" || faults == "both"
			if strings.Contains(err.Error(), "retain stop-observation.json") != wantObservation || strings.Contains(err.Error(), "retain terminal.json") != wantTerminal {
				t.Fatal(err)
			}
			if !wantTerminal {
				if _, err := regular(filepath.Join(dir, "terminal.json"), true); err != nil {
					t.Fatal("snapshot failure prevented STOP", err)
				}
			}
		})
	}
}

// Exercise public entry points with a genuinely approvable compiled plan. No
// kubeconfig/tools/network exist in this fixture: terminal rejection must precede them.
func TestPublicProbeAndDeployRejectTerminalEvidence(t *testing.T) {
	c, rs := compiledRollout(t)
	for _, marker := range []string{"final.json", "terminal.json", "probe-started.json", "both"} {
		t.Run(marker, func(t *testing.T) {
			w, p := sequenceFixture(t)
			w.Context = c
			intent, err := workload.ReadIntent("../../examples/s2")
			if err != nil {
				t.Fatal(err)
			}
			w.Model, err = workload.Resolve(intent, true)
			if err != nil {
				t.Fatal(err)
			}
			w.BinarySHA256 = c.CompilerSHA256
			w.BuildSource = strings.Repeat("e", 40)
			w.BuildGoVersion = "fixture-go"
			w.Install.ProductDigest = c.ProductSHA256
			w.Install.Record.FullCommit = p.BaseCommit
			w.Install.Record.ClusterUID = "fixture-cluster"
			w.Install.Record.InstallID = c.InstallID
			w.Install.Record.CertificateSHA256 = c.CertificateSHA256
			p.CompilerSHA256, p.ImplementationCommit, p.GoVersion = w.BinarySHA256, w.BuildSource, w.BuildGoVersion
			p.ProductSHA256, p.ClusterUID, p.InstallID = c.ProductSHA256, w.Install.Record.ClusterUID, c.InstallID
			p.Repository, p.Branch, p.CertificateSHA256 = c.Repository, c.Branch, c.CertificateSHA256
			p.ConfigSHA256, p.IntentSHA256 = workload.Digest(workload.JSON(w.Config)), workload.Digest(workload.JSON(w.Model.Intent))
			p.Project = w.Model.Intent.Project.Name
			for phase, r := range rs {
				p.PhaseSHA256[phase] = platform.BundleDigest(r.Files)
			}
			approval := workload.Digest(workload.JSON(p))
			if err := w.approve(p, approval); err != nil {
				t.Fatal("invalid test plan", err)
			}
			dir := filepath.Join(w.Config.StateDirectory, "authority", approval)
			names := []string{marker}
			if marker == "both" {
				names = []string{"final.json", "terminal.json"}
			}
			for _, name := range names {
				if err := save(filepath.Join(dir, name), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]string{"final.json": "already completed", "terminal.json": "prior attempt stopped", "probe-started.json": "intent already exists", "both": "contradictory attempt evidence"}[marker]
			if err := w.Probe(context.Background(), p, approval); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("public probe did not reject before effects", err)
			}
			if marker != "final.json" {
				if err := w.Deploy(context.Background(), p, approval); err == nil || !strings.Contains(err.Error(), want) {
					t.Fatal("deploy did not reject terminal contradiction/incomplete intent", err)
				}
			}
		})
	}
}

func TestProbeAttemptReadRetriesKeepOneFunctionalClaim(t *testing.T) {
	w, receipt, dir := probeAttemptFixture(t)
	windows, effects := 0, 0
	reads := [2]int{}
	read := func(ctx context.Context) (Observation, error) {
		window := windows
		windows++
		return w.waitObservation(ctx, probeReport("uid"), 0, func(context.Context) (Observation, error) {
			reads[window]++
			if reads[window] < 3 {
				return probeReport("uid"), Pending("closing capture changed")
			}
			return probeReport("uid"), nil
		})
	}
	err := w.runProbe(context.Background(), receipt, workload.Inventory{}, read, func(context.Context) (Object, error) { effects++; return Object{"roundtrip": "PASS"}, nil })
	if err != nil || effects != 1 || windows != 2 || reads != [2]int{3, 3} {
		t.Fatal(err, effects, windows, reads)
	}
	b, err := regular(filepath.Join(dir, "final.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	var final Object
	if err = json.Unmarshal(b, &final); err != nil || final["result"] != "PASS" || at(final, "observation", "runtime") != "VERIFIED" {
		t.Fatal(final, err)
	}
}

func TestProbeFailedPreObservationDoesNotClaimExecution(t *testing.T) {
	w, receipt, dir := probeAttemptFixture(t)
	failure := errors.New("pre-observation rejected")
	effects := 0
	err := w.runProbe(context.Background(), receipt, workload.Inventory{}, func(context.Context) (Observation, error) { return probeReport("uid"), failure }, func(context.Context) (Object, error) { effects++; return Object{}, nil })
	if !errors.Is(err, failure) || effects != 0 {
		t.Fatal(err, effects)
	}
	if _, err = regular(filepath.Join(dir, "probe-started.json"), true); !os.IsNotExist(err) {
		t.Fatal("read-only failure claimed functional execution", err)
	}
}

func TestProbeDoesNotTreatDanglingEvidenceAsAbsence(t *testing.T) {
	for _, marker := range []string{"final.json", "terminal.json", "probe-started.json"} {
		t.Run(marker, func(t *testing.T) {
			w, receipt, dir := probeAttemptFixture(t)
			if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, marker)); err != nil {
				t.Fatal(err)
			}
			effects := 0
			err := w.runProbe(context.Background(), receipt, workload.Inventory{}, func(context.Context) (Observation, error) { return probeReport("uid"), nil }, func(context.Context) (Object, error) { effects++; return Object{}, nil })
			if err == nil || effects != 0 {
				t.Fatal(err, effects)
			}
		})
	}
}
