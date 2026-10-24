package workloadrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/workload"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (w *Workflow) application() bool { return w.Config.Purpose == "application" }

// Read-only runtime prerequisites, with no demo endpoint, exec, S3 mutation or
// permission probe. Readiness is explicitly weaker than functional acceptance.
func (w *Workflow) applicationReady(ctx context.Context) error {
	for _, v := range w.Model.Intent.Workloads {
		if _, err := w.readyPod(ctx, v); err != nil {
			return err
		}
		client, err := w.httpsClient(v.Exposure.Hostname)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", "https://"+v.Exposure.Hostname+"/readyz", nil)
		if err != nil {
			client.CloseIdleConnections()
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			client.CloseIdleConnections()
			return fmt.Errorf("%s HTTPS readiness unavailable", v.Name)
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		closeErr := resp.Body.Close()
		client.CloseIdleConnections()
		if resp.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
			return fmt.Errorf("%s HTTPS readiness failed", v.Name)
		}
	}
	return w.metrics(ctx)
}

func (w *Workflow) finishApplication(ctx context.Context, p Plan, approval string) error {
	if !w.application() {
		return errors.New("ordinary application mode required")
	}
	if len(w.Model.Intent.Bindings) > 0 {
		if err := w.VerifyMaterialized(ctx); err != nil {
			return err
		}
	}
	if err := w.applicationReady(ctx); err != nil {
		return err
	}
	result, err := w.Compile("consumer")
	if err != nil {
		return err
	}
	parent := p.Parent
	for _, phase := range p.Phases[:len(p.Phases)-1] {
		receipt, err := w.readPublication(p, phase, parent, p.PhaseSHA256[phase])
		if err != nil {
			return err
		}
		parent = receipt.Commit
	}
	// The closing read uses the normal receipt-bound Gate, including UID and
	// ownership. No functional PASS or synthetic probe result is manufactured.
	b, err := regular(w.publicationPath(p, "consumer"), true)
	if err != nil {
		return err
	}
	var receipt Publication
	if err = workload.StrictDecode(b, &receipt); err != nil {
		return err
	}
	if receipt.Parent != parent || receipt.PlanSHA256 != approval {
		return errors.New("consumer receipt differs")
	}
	report, err := w.ObserveFor(ctx, result, receipt.Commit, 15*time.Minute)
	if err != nil {
		return err
	}
	out := Object{"result": "DEPLOYED", "functional": "UNPROVEN", "planSHA256": approval, "deploymentCommit": receipt.Commit,
		"compilerSHA256": w.BinarySHA256, "observation": report}
	return w.commitApplication(approval, out)
}

// Necessary predecessor state first; final.json is the last success commit.
func (w *Workflow) commitApplication(approval string, out Object) error {
	if len(w.Model.Intent.Bindings) > 0 {
		provider, err := regular(filepath.Join(w.Config.StateDirectory, "provider-prepared.json"), true)
		if err != nil {
			return err
		}
		if err = save(filepath.Join(w.Config.StateDirectory, "provider.json"), provider, false); err != nil {
			return err
		}
	}
	return save(filepath.Join(w.Config.StateDirectory, "authority", approval, "final.json"), workload.JSON(out), true)
}

// A new review cannot bury an incomplete external attempt by overwriting the
// current plan index. Pure preparation without authority evidence may be replaced.
func (w *Workflow) PlanningAllowed() error {
	p, err := w.ReadPlan()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := filepath.Join(w.Config.StateDirectory, "authority", workload.Digest(workload.JSON(p)))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	complete, err := attemptComplete(dir)
	if err != nil {
		return err
	}
	if !complete {
		return errors.New("incomplete recorded attempt; inspect app status before a new execution decision")
	}
	raw, err := regular(filepath.Join(dir, "final.json"), true)
	if err != nil {
		return err
	}
	var f Object
	if err = workload.StrictDecode(raw, &f); err != nil {
		return err
	}
	if f["planSHA256"] != workload.Digest(workload.JSON(p)) || f["compilerSHA256"] != p.CompilerSHA256 || f["result"] != "DEPLOYED" && f["result"] != "PASS" {
		return errors.New("completed predecessor binding differs")
	}
	return nil
}

func (w *Workflow) validateApplicationFinal(p Plan) error {
	digest := workload.Digest(workload.JSON(p))
	raw, err := regular(filepath.Join(w.Config.StateDirectory, "authority", digest, "final.json"), true)
	if err != nil {
		return err
	}
	var f Object
	if err = workload.StrictDecode(raw, &f); err != nil {
		return err
	}
	raw, err = regular(w.publicationPath(p, "consumer"), true)
	if err != nil {
		return err
	}
	var receipt Publication
	if err = workload.StrictDecode(raw, &receipt); err != nil {
		return err
	}
	if f["result"] != "DEPLOYED" || f["functional"] != "UNPROVEN" || f["planSHA256"] != digest || f["compilerSHA256"] != p.CompilerSHA256 || f["deploymentCommit"] != receipt.Commit || receipt.PlanSHA256 != digest {
		return errors.New("ordinary completion record differs from plan/receipt")
	}
	return nil
}
