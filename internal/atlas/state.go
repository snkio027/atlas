package atlas

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
)

type State string

const (
	Absent      State = "ABSENT"
	Fresh       State = "FRESH"
	Handoff     State = "HANDOFF_PENDING"
	Adopted     State = "ADOPTED"
	Degraded    State = "ADOPTED_DEGRADED"
	Drifted     State = "DRIFTED"
	Unavailable State = "UNAVAILABLE"
)

type Report struct {
	State  State  `json:"state"`
	Detail string `json:"detail"`
}
type Live struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		UID             string            `json:"uid"`
		Annotations     map[string]string `json:"annotations"`
		Finalizers      []string          `json:"finalizers"`
		OwnerReferences []Object          `json:"ownerReferences"`
		ManagedFields   []struct {
			Manager, Operation, Subresource string
			FieldsV1                        map[string]any `json:"fieldsV1"`
		} `json:"managedFields"`
	} `json:"metadata"`
	Immutable bool              `json:"immutable"`
	Data      map[string]string `json:"data"`
	Spec      Object            `json:"spec"`
	Status    struct {
		Sync           struct{ Status, Revision string } `json:"sync"`
		Health         struct{ Status string }           `json:"health"`
		OperationState struct {
			Phase      string
			SyncResult struct {
				Revision  string
				Resources []struct{ Group, Version, Kind, Namespace, Name, Status, SyncPhase string }
			} `json:"syncResult"`
		} `json:"operationState"`
		Resources []struct{ Group, Version, Kind, Namespace, Name, Status string } `json:"resources"`
	} `json:"status"`
	Operation any `json:"operation"`
}
type observation struct {
	report                       Report
	identity, root, self, signal *Live
}

func (a *App) kube(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	data, e := readFile(a.Root, ".state/kubeconfig")
	if e != nil {
		return nil, e
	}
	expected, e := readFile(a.Root, ".state/kubeconfig.sha256")
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(string(expected)) != digest(data) {
		return nil, errors.New("kubeconfig changed; target reauthorization required")
	}
	p, e := safePath(a.Root, ".state/kubeconfig")
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(p)
	if e != nil {
		return nil, e
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("kubeconfig must be owner-only")
	}
	prefix := []string{"--kubeconfig", p, "--context", "kind-" + a.Config.Cluster, "--request-timeout=30s"}
	return a.Runner.Run(ctx, Request{Tool: "kubectl", Args: append(prefix, args...), Input: input})
}

func (a *App) get(ctx context.Context, kind, namespace, name string) (*Live, error) {
	args := []string{"get", kind, name, "--ignore-not-found=true", "-o", "json"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	b, e := a.kube(ctx, nil, args...)
	if e != nil {
		return nil, e
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var obj Live
	if e = decode(b, &obj); e != nil {
		return nil, e
	}
	if obj.Metadata.Name != name || obj.Metadata.Namespace != namespace || obj.Metadata.UID == "" {
		return nil, errors.New("malformed or mismatched API object")
	}
	wantKinds := map[string]string{"configmap": "ConfigMap", "namespace": "Namespace", "crd": "CustomResourceDefinition", "application": "Application", "appproject": "AppProject"}
	if expected, ok := wantKinds[kind]; !ok || obj.Kind != expected {
		return nil, errors.New("API object kind does not match request")
	}
	return &obj, nil
}
func (a *App) create(ctx context.Context, o Object) error {
	_, e := a.kube(ctx, jsonBytes(o), "create", "-f", "-")
	return e
}
func dataMatches(o *Live, want map[string]string) bool {
	return o != nil && o.Kind == "ConfigMap" && o.Immutable && len(o.Metadata.OwnerReferences) == 0 && reflect.DeepEqual(o.Data, want)
}
func specMatches(o *Live, want Object) bool {
	if o == nil || len(o.Metadata.Finalizers) > 0 || len(o.Metadata.OwnerReferences) > 0 {
		return false
	}
	var normalized Object
	_ = decode(jsonBytes(want["spec"]), &normalized)
	return reflect.DeepEqual(o.Spec, normalized)
}
func ready(o *Live) bool {
	return o != nil && o.Status.Sync.Status == "Synced" && o.Status.Health.Status == "Healthy" && o.Operation == nil && o.Status.OperationState.Phase != "Running" && o.Status.OperationState.Phase != "Terminating"
}
func (a *App) validSignal(signal *Live) bool {
	return dataMatches(signal, map[string]string{"fingerprint": a.fingerprint()}) && signal.Metadata.Annotations["argocd.argoproj.io/tracking-id"] == "argocd-self:/ConfigMap:"+a.argoNamespace()+"/atlas-refactor-adoption-signal"
}

func (a *App) clusterExists(ctx context.Context) (bool, error) {
	b, e := a.run(ctx, "kind", "get", "clusters")
	if e != nil {
		return false, e
	}
	for _, name := range strings.Fields(string(b)) {
		if name == a.Config.Cluster {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) inspect(ctx context.Context) (observation, error) {
	state := func(s State, detail string) (observation, error) { return observation{report: Report{s, detail}}, nil }
	exists, e := a.clusterExists(ctx)
	if e != nil {
		return observation{}, e
	}
	if !exists {
		return state(Absent, "test cluster does not exist")
	}
	identity, e := a.get(ctx, "configmap", "kube-system", "atlas-refactor-identity")
	if e != nil {
		return observation{}, e
	}
	if !dataMatches(identity, a.identity()) {
		return state(Drifted, "existing cluster identity does not match; no mutation permitted")
	}
	latch, e := a.get(ctx, "configmap", "kube-system", "atlas-refactor-handoff")
	if e != nil {
		return observation{}, e
	}
	receipt, e := a.get(ctx, "configmap", "kube-system", "atlas-refactor-receipt")
	if e != nil {
		return observation{}, e
	}
	binding := map[string]string{"identityUID": identity.Metadata.UID, "fingerprint": a.fingerprint()}
	if latch != nil && !dataMatches(latch, binding) {
		return state(Drifted, "invalid handoff latch")
	}
	if receipt != nil && (latch == nil || !receipt.Immutable || receipt.Kind != "ConfigMap" || len(receipt.Metadata.OwnerReferences) > 0 || receipt.Data["identityUID"] != identity.Metadata.UID || receipt.Data["fingerprint"] != a.fingerprint() || receipt.Data["rootUID"] == "" || receipt.Data["selfUID"] == "" || receipt.Data["signalUID"] == "" || len(receipt.Data) != 5) {
		return state(Drifted, "invalid adoption receipt")
	}
	crd, e := a.get(ctx, "crd", "", "applications.argoproj.io")
	if e != nil {
		return observation{}, e
	}
	if crd == nil {
		if receipt != nil {
			return state(Degraded, "adopted; Argo CRD missing")
		}
		if latch != nil {
			return state(Drifted, "handoff started; Argo CRD missing; recovery required")
		}
		return state(Fresh, "owned cluster; Seed authority available")
	}
	root, e := a.get(ctx, "application", a.argoNamespace(), "atlas-refactor-root")
	if e != nil {
		return observation{}, e
	}
	signal, e := a.get(ctx, "configmap", a.argoNamespace(), "atlas-refactor-adoption-signal")
	if e != nil {
		return observation{}, e
	}
	self, e := a.get(ctx, "application", a.argoNamespace(), "argocd-self")
	if e != nil {
		return observation{}, e
	}
	if latch == nil {
		if root != nil || signal != nil || self != nil {
			return state(Drifted, "GitOps evidence without handoff latch; Seed denied")
		}
		return state(Fresh, "owned cluster; Seed authority available")
	}
	if root == nil {
		if receipt != nil {
			return state(Degraded, "adopted; Root missing; normal Bootstrap cannot repair it")
		}
		return state(Drifted, "Root missing after handoff intent; recovery required")
	}
	if !specMatches(root, a.rootApplication()) || root.Metadata.Annotations["argocd.argoproj.io/tracking-id"] != "" {
		return state(Drifted, "External Root drift; no overwrite permitted")
	}
	if self != nil && !specMatches(self, a.selfApplication()) {
		return state(Drifted, "argocd-self spec drift")
	}
	if signal != nil && !a.validSignal(signal) {
		return state(Drifted, "invalid GitOps adoption signal")
	}
	project, e := a.get(ctx, "appproject", a.argoNamespace(), "atlas-bootstrap")
	if e != nil {
		return observation{}, e
	}
	if !specMatches(project, a.project("atlas-bootstrap", false)) {
		return state(Drifted, "bootstrap project drift; no repair permitted")
	}
	if receipt != nil {
		// Schema 3 separates durable authority from current runtime health.
		// Observation/Gate-B verifies nodes and workloads after handoff.
		if !a.Config.fourNodeProfile() {
			if e := a.VerifyNodes(ctx); e != nil {
				return state(Degraded, "adopted; node inventory or readiness invalid: "+e.Error())
			}
		}
		if root.Metadata.UID != receipt.Data["rootUID"] || (self != nil && self.Metadata.UID != receipt.Data["selfUID"]) || (signal != nil && signal.Metadata.UID != receipt.Data["signalUID"]) {
			return state(Drifted, "adoption UID contradiction")
		}
		complete, e := a.handoffComplete(ctx, root, self, signal, true)
		if e != nil {
			return observation{}, e
		}
		if !complete {
			return state(Degraded, "adopted; GitOps is degraded; Seed remains denied")
		}
		detail := "GitOps owns reconciliation"
		if a.Config.fourNodeProfile() {
			detail = "durable GitOps handoff verified; rollout is checked by Observation"
		}
		return observation{Report{Adopted, detail}, identity, root, self, signal}, nil
	}
	inventory := false
	if self != nil {
		for _, r := range self.Status.Resources {
			if r.Group == "" && r.Kind == "ConfigMap" && r.Namespace == a.argoNamespace() && r.Name == "atlas-refactor-adoption-signal" {
				inventory = true
			}
		}
	}
	if inventory {
		complete, e := a.handoffComplete(ctx, root, self, signal, false)
		if e != nil {
			return observation{}, e
		}
		if complete {
			return observation{Report{Handoff, "GitOps revision and Seed ownership verified; receipt can be committed"}, identity, root, self, signal}, nil
		}
	}
	return state(Handoff, "waiting for GitOps reconciliation; Seed authority is permanently denied")
}

func (a *App) Status(ctx context.Context) Report {
	if e := a.VerifyArtifacts(); e != nil {
		return Report{Unavailable, e.Error()}
	}
	if e := a.verifyTools(ctx, true); e != nil {
		return Report{Unavailable, e.Error()}
	}
	observation, e := a.inspect(ctx)
	if e != nil {
		return Report{Unavailable, e.Error()}
	}
	return observation.report
}
