package installation

import (
	"atlas-refactor/internal/atlas"
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func (w *Workflow) Install(ctx context.Context, approval string) (result error) {
	defer func() {
		if result != nil {
			if e := save(filepath.Join(w.Config.StateDirectory, "latest-attempt.json"), JSON(map[string]any{"result": "STOP", "exitCode": 1, "message": result.Error(), "record": w.Record}), false); e != nil {
				result = errors.Join(result, e)
			}
		}
	}()
	planHash := Digest(w.PlanJSON())
	if approval != planHash {
		return errors.New("install requires --approve-plan with the displayed exact plan SHA256")
	}
	if w.Record.ApprovedPlan != "" && w.Record.ApprovedPlan != planHash {
		return errors.New("approved installation scope changed")
	}
	if w.Record.Complete {
		return w.Verify(ctx, false)
	}
	if e := w.CheckPlan(ctx); e != nil {
		return e
	}
	w.Record.ApprovedPlan = planHash
	if e := w.saveRecord(); e != nil {
		return e
	}
	a, e := w.app(ctx, w.Record.BaseCommit)
	if e != nil {
		return e
	}
	base, e := a.Render(ctx)
	if e != nil {
		return e
	}
	w.message("Publishing the base GitOps deployment")
	if w.Record.BaseCommit == "" {
		w.Record.BaseCommit, e = w.publish(ctx, "base", base, "")
		if e != nil {
			return e
		}
		if e = w.saveRecord(); e != nil {
			return e
		}
	}
	// A crash after full publication but before saving FullCommit must not
	// send the installer back to the base deployment's mutation gate.
	if w.Record.FullCommit == "" {
		full, e := w.reconcileFullPublication(ctx, base[signalPath])
		if e != nil {
			return e
		}
		if full != "" {
			w.Record.FullCommit = full
			if e = w.saveRecord(); e != nil {
				return e
			}
		}
	}
	a, e = w.app(ctx, w.Record.BaseCommit)
	if e != nil {
		return e
	}
	if w.Record.FullCommit == "" {
		w.message("Creating four nodes and completing finite Bootstrap handoff")
		if e = a.Apply(ctx, w.Config.Cluster, true); e != nil {
			return e
		}
		if e = w.bindCluster(ctx); e != nil {
			return e
		}
		w.message("Verifying this installation's Trust Root backup before enabling consumers")
		if e = w.backup(ctx); e != nil {
			return e
		}
		sealed, e := w.credentials(ctx)
		if e != nil {
			return e
		}
		full, e := w.Product.Project(w.Config, true)
		if e != nil {
			return e
		}
		full[CredentialPath] = sealed
		full[signalPath] = base[signalPath]
		w.Record.FullCommit, e = w.publish(ctx, "full", full, w.Record.BaseCommit)
		if e != nil {
			return e
		}
		if e = w.saveRecord(); e != nil {
			return e
		}
	}
	if e = w.bindCluster(ctx); e != nil {
		return e
	}
	if e = w.verifyAuthority(ctx); e != nil {
		return e
	}
	w.message("Notifying GitOps controllers and waiting for the full platform")
	full, e := w.desiredFull()
	if e != nil {
		return e
	}
	if e = w.notify(ctx, full); e != nil {
		return e
	}
	if e = w.waitApplications(ctx, full); e != nil {
		return e
	}
	if e = w.waitRuntime(ctx); e != nil {
		return e
	}
	if e = w.Verify(ctx, true); e != nil {
		return e
	}
	evidence, e := w.finalEvidence(ctx, planHash)
	if e != nil {
		return e
	}
	if e = save(filepath.Join(w.Config.StateDirectory, "authority", "final.json"), evidence, true); e != nil {
		return e
	}
	w.Record.Complete = true
	return w.saveRecord()
}
func (w *Workflow) desiredFull() (Files, error) {
	f, e := w.Product.Project(w.Config, true)
	if e != nil {
		return nil, e
	}
	b, e := privateRead(filepath.Join(w.Config.StateDirectory, "sealed.json"))
	if e != nil {
		return nil, e
	}
	var s SealedRecord
	if e = Decode(b, &s); e != nil {
		return nil, e
	}
	if s.CertificateSHA256 != w.Record.CertificateSHA256 || s.CredentialsSHA256 != w.Record.CredentialsSHA256 {
		return nil, errors.New("sealed instance binding differs")
	}
	f[CredentialPath] = JSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": s.Objects})
	if Digest(f[CredentialPath]) != w.Record.SealedSHA256 {
		return nil, errors.New("sealed payload changed")
	}
	return f, nil
}
func applications(f Files) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, p := range []string{RootPath + "/resources.json", appCatalog, "gitops/workloads/applications/overlays/development/resources.json", bootstrapDir + "root.json"} {
		objects, e := platform.DecodeJSONManifests(f[p])
		if e != nil {
			return nil, e
		}
		for _, o := range objects {
			m, _ := o["metadata"].(map[string]any)
			name, _ := m["name"].(string)
			if name == "" || o["kind"] != "Application" || out[name] != nil {
				return nil, errors.New("invalid expected Application inventory")
			}
			out[name] = o
		}
	}
	return out, nil
}
func (w *Workflow) getApp(ctx context.Context, name string) (map[string]any, error) {
	b, e := w.kube(ctx, nil, "get", "application", name, "-n", platformnames.Argo, "--ignore-not-found=true", "-o", "json")
	if e != nil {
		return nil, e
	}
	if len(b) == 0 {
		return nil, nil
	}
	var o map[string]any
	e = decodeLive(b, &o)
	return o, e
}
func nested(o map[string]any, keys ...string) any {
	var x any = o
	for _, k := range keys {
		m, _ := x.(map[string]any)
		x = m[k]
	}
	return x
}
func (w *Workflow) waitApplications(ctx context.Context, f Files) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	for {
		if e := w.checkApplications(ctx, f); e == nil {
			return nil
		} else {
			var pending pendingState
			if !errors.As(e, &pending) {
				return e
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("platform did not converge: %w", ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}
func (w *Workflow) checkApplications(ctx context.Context, f Files) error {
	w.applications = nil
	proof := map[string]map[string]string{}
	apps, e := applications(f)
	if e != nil {
		return e
	}
	b, e := w.kube(ctx, nil, "get", "applications", "-n", platformnames.Argo, "-o", "json")
	if e != nil {
		return e
	}
	var list struct {
		Kind  string
		Items []map[string]any
	}
	if e = decodeLive(b, &list); e != nil {
		return e
	}
	if list.Kind != "List" && list.Kind != "ApplicationList" {
		return errors.New("invalid Application list response")
	}
	if len(list.Items) > len(apps) {
		return errors.New("unexpected Application inventory")
	}
	seen := map[string]bool{}
	var waiting pendingState
	for _, live := range list.Items {
		name, _ := nested(live, "metadata", "name").(string)
		if apps[name] == nil || seen[name] || !reflect.DeepEqual(live["spec"], apps[name]["spec"]) {
			return errors.New("Application spec/inventory drift: " + name)
		}
		uid, _ := nested(live, "metadata", "uid").(string)
		if uid == "" {
			return errors.New("Application lacks UID: " + name)
		}
		proof[name] = map[string]string{"uid": uid, "revision": w.Record.FullCommit, "sync": "Synced", "health": "Healthy"}
		seen[name] = true
		if nested(live, "status", "sync", "revision") != w.Record.FullCommit || nested(live, "status", "sync", "status") != "Synced" || nested(live, "status", "health", "status") != "Healthy" || live["operation"] != nil {
			waiting = pendingState("Application not converged: " + name)
		}
		for _, c := range array(nested(live, "status", "conditions")) {
			kind, _ := nested(mapping(c), "type").(string)
			if strings.HasSuffix(kind, "Error") {
				return errors.New("Application error: " + name)
			}
		}
	}
	if waiting != "" {
		return waiting
	}
	if len(seen) != len(apps) {
		return pendingState("waiting for declared Applications")
	}
	w.applications = proof
	return nil
}
func mapping(x any) map[string]any { m, _ := x.(map[string]any); return m }
func array(x any) []any            { a, _ := x.([]any); return a }

// The durable Bootstrap engine validates its base contract after full rollout;
// its published commit is irrelevant to the permanent relinquishment of Seed.
func (w *Workflow) verifyAuthority(ctx context.Context) error {
	a, e := w.app(ctx, w.Record.BaseCommit)
	if e != nil {
		return e
	}
	report := a.Status(ctx)
	if report.State != atlas.Adopted {
		return fmt.Errorf("Bootstrap authority: %s: %s", report.State, report.Detail)
	}
	return a.VerifyNodes(ctx)
}

type pendingState string

func (e pendingState) Error() string { return string(e) }

func (w *Workflow) waitRuntime(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		e := w.Verify(ctx, false)
		if e == nil {
			return nil
		}
		var pending pendingState
		if !errors.As(e, &pending) {
			return e
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("runtime readiness timed out (%s): %w", pending, ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}
