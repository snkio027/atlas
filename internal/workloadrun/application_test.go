package workloadrun

import (
	"context"
	"github.com/snkio027/atlas/internal/workload"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appFixture(t *testing.T) *Workflow {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return &Workflow{Config: Config{Purpose: "application", StateDirectory: d}, Model: &workload.Model{Intent: workload.Intent{Bindings: []workload.Binding{{Name: "storage"}}}}}
}
func TestApplicationNeverEntersAcceptanceProbe(t *testing.T) {
	w := appFixture(t)
	err := w.Probe(context.Background(), Plan{}, "")
	if err == nil || !strings.Contains(err.Error(), "no platform acceptance probe") {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(w.Config.StateDirectory)
	if len(entries) != 0 {
		t.Fatal("application probe produced local or external effects")
	}
}
func TestApplicationFinalCommitsOnlyAfterProvider(t *testing.T) {
	for _, failure := range []string{"missing-prepared", "blocked-provider", "none"} {
		t.Run(failure, func(t *testing.T) {
			w := appFixture(t)
			d := w.Config.StateDirectory
			if failure != "missing-prepared" {
				if err := save(filepath.Join(d, "provider-prepared.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "blocked-provider" {
				if err := os.Mkdir(filepath.Join(d, "provider.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := w.commitApplication("plan", Object{"result": "DEPLOYED", "functional": "UNPROVEN"})
			if (err == nil) != (failure == "none") {
				t.Fatal(err)
			}
			final := filepath.Join(d, "authority/plan/final.json")
			raw, e := os.ReadFile(final)
			if failure != "none" {
				if !os.IsNotExist(e) {
					t.Fatal("success marker survived failed provider commit")
				}
				return
			}
			if e != nil || strings.Contains(string(raw), "PASS") || !strings.Contains(string(raw), "UNPROVEN") {
				t.Fatal(string(raw), e)
			}
			if _, e = os.ReadFile(filepath.Join(d, "provider.json")); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestNewPlanCannotBuryPartialOrStoppedAttempt(t *testing.T) {
	for _, state := range []string{"prepared", "partial", "stop", "complete", "contradiction", "wrong-final"} {
		t.Run(state, func(t *testing.T) {
			w := appFixture(t)
			p := Plan{Schema: 2, CompilerSHA256: "compiler", PhaseSHA256: map[string]string{}, CredentialTargets: []string{}, Phases: []string{}}
			if err := w.SavePlan(p); err != nil {
				t.Fatal(err)
			}
			digest := workload.Digest(workload.JSON(p))
			dir := filepath.Join(w.Config.StateDirectory, "authority", digest)
			if state != "prepared" {
				if err := save(filepath.Join(dir, "image.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			}
			if state == "stop" || state == "contradiction" {
				if err := save(filepath.Join(dir, "terminal.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			}
			if state == "complete" || state == "contradiction" || state == "wrong-final" {
				sha := digest
				if state == "wrong-final" {
					sha = "wrong"
				}
				if err := save(filepath.Join(dir, "final.json"), workload.JSON(Object{"result": "DEPLOYED", "planSHA256": sha, "compilerSHA256": "compiler"}), true); err != nil {
					t.Fatal(err)
				}
			}
			err := w.PlanningAllowed()
			if (err == nil) != (state == "prepared" || state == "complete") {
				t.Fatal(state, err)
			}
		})
	}
}
func TestOrdinaryCRIImportUsesOwnRepositoryAndDigest(t *testing.T) {
	ref := "atlas.local/experiment-archive:v2@sha256:" + strings.Repeat("e", 64)
	canonical := "atlas.local/experiment-archive@sha256:" + strings.Repeat("e", 64)
	calls := 0
	err := importWebImage(func(_ []byte, args ...string) ([]byte, error) {
		calls++
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "s2-web") || strings.Contains(joined, "--force") {
			t.Fatal("fixture alias/overwrite", joined)
		}
		if strings.Contains(joined, "images list") {
			return []byte("atlas.local/experiment-archive:v2"), nil
		}
		if strings.Contains(joined, "inspecti") {
			return workload.JSON(Object{"status": Object{"repoDigests": []string{canonical}}}), nil
		}
		return nil, nil
	}, "node", []byte("verified"), ref)
	if err != nil || calls != 4 {
		t.Fatal(err, calls)
	}
}

func TestRecordedObservationCannotApproveAnyMutation(t *testing.T) {
	w := appFixture(t)
	p := Plan{ConfigSHA256: workload.Digest(workload.JSON(w.Config)), IntentSHA256: workload.Digest(workload.JSON(w.Model.Intent)), CompilerSHA256: w.BinarySHA256}
	view, err := w.ObservationView(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = view.approve(p, workload.Digest(workload.JSON(p))); err == nil || !strings.Contains(err.Error(), "no mutation authority") {
		t.Fatal(err)
	}
	p.IntentSHA256 = "changed"
	if _, err = w.ObservationView(p); err == nil {
		t.Fatal("unbound observation accepted")
	}
}

func TestStatusReportsUnknownWriteBeforeAttemptingLiveReads(t *testing.T) {
	w := appFixture(t)
	p := Plan{Schema: 2, ConfigSHA256: workload.Digest(workload.JSON(w.Config)), IntentSHA256: workload.Digest(workload.JSON(w.Model.Intent)), PhaseSHA256: map[string]string{}, CredentialTargets: []string{}, Phases: []string{"consumer"}}
	if err := w.SavePlan(p); err != nil {
		t.Fatal(err)
	}
	digest := workload.Digest(workload.JSON(p))
	dir := filepath.Join(w.Config.StateDirectory, "authority", digest)
	if err := save(filepath.Join(dir, "consumer.json.intent"), []byte("{}"), true); err != nil {
		t.Fatal(err)
	}
	if err := save(filepath.Join(dir, "terminal.json"), []byte("{}"), true); err != nil {
		t.Fatal(err)
	}
	s, err := w.Current(context.Background())
	if err == nil || err.Error() != "unreceipted publication intent" || s.HistoricalResult != "STOP_OR_CONTRADICTION" || len(s.UncertainWrites) != 1 || s.Authority != "UNKNOWN" {
		t.Fatal(s, err)
	}
}
