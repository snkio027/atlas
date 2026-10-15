package atlas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

func (a *App) sourceCommit(ctx context.Context) (string, error) {
	if a.resolvedCommit != "" {
		return a.resolvedCommit, nil
	}
	remote, e := a.run(ctx, "git", "ls-remote", "--exit-code", a.Config.RepositoryURL, a.Config.Revision)
	if e != nil {
		return "", errors.New("GitOps source is not reachable; publish the reviewed commit first")
	}
	fields := strings.Fields(string(remote))
	if len(fields) != 2 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(fields[0]) {
		return "", errors.New("GitOps source must resolve to one exact commit")
	}
	a.resolvedCommit = fields[0]
	return a.resolvedCommit, nil
}

func (a *App) handoffComplete(ctx context.Context, root, self, signal *Live, adopted bool) (bool, error) {
	if a.development != nil {
		return a.developmentHandoffComplete(ctx, root, self, signal, adopted)
	}
	if signal == nil || !ready(root) || !ready(self) {
		return false, nil
	}
	commit, e := a.sourceCommit(ctx)
	if e != nil {
		return false, e
	}
	if root.Status.Sync.Revision != commit || self.Status.Sync.Revision != commit {
		return false, nil
	}
	for _, child := range []struct{ name, project, path, wave string }{
		{"project-bootstrap", "atlas-bootstrap", "/projects", "-20"},
		{"platform-control", "platform-project", "/platform/applications", "-10"},
	} {
		live, e := a.get(ctx, "application", a.argoNamespace(), child.name)
		if e != nil {
			return false, e
		}
		if !specMatches(live, a.application(child.name, child.project, a.Config.GitOpsPath+child.path, child.wave)) || !ready(live) || live.Status.Sync.Revision != commit {
			return false, nil
		}
	}
	return a.seedOwnedByArgo(ctx, self, commit)
}

func seedKey(o *Live) string {
	group := ""
	if strings.Contains(o.APIVersion, "/") {
		group = strings.SplitN(o.APIVersion, "/", 2)[0]
	}
	return group + "/" + o.Kind + ":" + o.Metadata.Namespace + "/" + o.Metadata.Name
}

func seedList(data []byte) ([]Live, error) {
	var list struct {
		Kind  string
		Items []Live
	}
	if e := decode(data, &list); e != nil {
		return nil, e
	}
	if list.Kind != "List" {
		return nil, errors.New("Seed projection must be a Kubernetes List")
	}
	return validateSeedObjects(list.Items)
}

// kubectl create -o json emits one JSON object per manifest, while kubectl get
// emits a List. Accept the client printer's stream without assuming a List.
func seedProjection(data []byte) ([]Live, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	objects := []Live{}
	for {
		var raw json.RawMessage
		if e := decoder.Decode(&raw); e != nil {
			if errors.Is(e, io.EOF) {
				break
			}
			return nil, e
		}
		var obj Live
		if e := decode(raw, &obj); e != nil {
			return nil, e
		}
		if obj.Kind == "List" {
			if len(objects) != 0 {
				return nil, errors.New("mixed Seed projection formats")
			}
			if e := decoder.Decode(&raw); !errors.Is(e, io.EOF) {
				return nil, errors.New("trailing data after Seed List")
			}
			return seedList(data)
		}
		objects = append(objects, obj)
	}
	return validateSeedObjects(objects)
}

func validateSeedObjects(objects []Live) ([]Live, error) {
	seen := map[string]bool{}
	for _, o := range objects {
		key := seedKey(&o)
		if o.APIVersion == "" || o.Kind == "" || o.Kind == "Secret" || o.Metadata.Name == "" || seen[key] {
			return nil, errors.New("malformed or duplicate Seed projection")
		}
		seen[key] = true
	}
	return objects, nil
}

// Argo CD 3.5.1 deliberately omits tracking metadata on CRDs. Require both
// current inventory and a successful exact-revision sync result instead.
func crdReconciled(self, want *Live, commit string) bool {
	if self == nil || self.Status.OperationState.Phase != "Succeeded" || self.Status.OperationState.SyncResult.Revision != commit {
		return false
	}
	group, version, ok := strings.Cut(want.APIVersion, "/")
	if !ok || group != "apiextensions.k8s.io" || want.Kind != "CustomResourceDefinition" || want.Metadata.Namespace != "" {
		return false
	}
	inventory, synced := 0, 0
	for _, r := range self.Status.Resources {
		if r.Group == group && r.Kind == want.Kind && r.Name == want.Metadata.Name {
			if r.Version != version || r.Namespace != "" || r.Status != "Synced" {
				return false
			}
			inventory++
		}
	}
	for _, r := range self.Status.OperationState.SyncResult.Resources {
		if r.Group == group && r.Kind == want.Kind && r.Name == want.Metadata.Name {
			if r.Version != version || r.Namespace != self.Metadata.Namespace || r.Status != "Synced" || r.SyncPhase != "Sync" {
				return false
			}
			synced++
		}
	}
	return inventory == 1 && synced == 1
}

func (a *App) seedOwnedByArgo(ctx context.Context, self *Live, commit string) (bool, error) {
	files, e := a.Render(ctx)
	if e != nil {
		return false, e
	}
	seed := files[a.seedFile()]
	return a.seedPayloadOwned(ctx, self, commit, seed)
}

func (a *App) seedPayloadOwned(ctx context.Context, self *Live, commit string, seed []byte) (bool, error) {
	return a.seedPayloadOwnership(ctx, self, commit, seed, true)
}

// Initial adoption needs correlated sync proof for untracked CRDs. After a valid
// schema-3 Receipt, continuing authority uses live SSA ownership; the latest
// operation and comparison belong to rollout verification instead.
func (a *App) seedPayloadOwnership(ctx context.Context, self *Live, commit string, seed []byte, requireInitialSync bool) (bool, error) {
	// This is local manifest decoding, not a create API request. It uses only
	// discovery reads with the bound kubeconfig; server dry-run is not permitted.
	b, e := a.kube(ctx, seed, "create", "--dry-run=client", "--validate=false", "-f", "-", "-o", "json")
	if e != nil {
		return false, e
	}
	desired, e := seedProjection(b)
	if e != nil {
		return false, e
	}
	b, e = a.kube(ctx, seed, "get", "-f", "-", "--ignore-not-found=true", "--show-managed-fields", "-o", "json")
	if e != nil {
		return false, e
	}
	live, e := seedList(b)
	if e != nil {
		return false, e
	}
	byKey := map[string]*Live{}
	for i := range live {
		byKey[seedKey(&live[i])] = &live[i]
	}
	count := 0
	for i := range desired {
		want := &desired[i]
		// Hook Jobs and their helper RBAC/ServiceAccounts may be replaced or
		// removed by Argo between syncs. They do not represent durable adoption.
		if want.Metadata.Annotations["helm.sh/hook"] != "" || want.Metadata.Annotations["argocd.argoproj.io/hook"] != "" {
			continue
		}
		count++
		key := seedKey(want)
		actual := byKey[key]
		if actual == nil || actual.Metadata.UID == "" {
			return false, nil
		}
		crd := want.APIVersion == "apiextensions.k8s.io/v1" && want.Kind == "CustomResourceDefinition"
		tracking := actual.Metadata.Annotations["argocd.argoproj.io/tracking-id"]
		if crd {
			if tracking != "" || requireInitialSync && !crdReconciled(self, want, commit) {
				return false, nil
			}
		} else {
			// Argo uses the destination namespace for cluster-scoped tracking IDs.
			tracked := *want
			if tracked.Metadata.Namespace == "" {
				tracked.Metadata.Namespace = self.Spec["destination"].(map[string]any)["namespace"].(string)
			}
			if tracking != self.Metadata.Name+":"+seedKey(&tracked) {
				return false, nil
			}
		}
		owned := false
		for _, field := range actual.Metadata.ManagedFields {
			_, ownsSpec := field.FieldsV1["f:spec"]
			if field.Manager == "argocd-controller" && field.Operation == "Apply" && field.Subresource == "" && (!crd || ownsSpec) {
				owned = true
			}
		}
		if !owned {
			return false, nil
		}
	}
	if count == 0 {
		return false, errors.New("Seed has no durable adoption inventory")
	}
	return true, nil
}
