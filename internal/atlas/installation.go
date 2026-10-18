package atlas

import (
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// InstallationBinding separates immutable product/instance identity from the
// mutable, explicitly published deployment commit. It is only supplied by the
// D1 installer, never decoded from the ordinary bootstrap CLI configuration.
type InstallationBinding struct {
	HTTPPort, HTTPSPort int
	BinarySHA256        string
	ProductSHA256       string
	InstallID           string
	DeploymentCommit    string
	Bundle              map[string][]byte
	Images              []string
}

func NewInstallation(root string, c Config, l Lock, r Runner, b InstallationBinding) (*App, error) {
	if c.Schema != 4 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(b.ProductSHA256) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(b.BinarySHA256) || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(b.InstallID) {
		return nil, errors.New("invalid D1 product/instance binding")
	}
	a := &App{Root: root, Config: c, Lock: l, Runner: r, installation: &b}
	return a, a.VerifyArtifacts()
}
func (a *App) prepareInstallation() error {
	if a.installation == nil {
		return errors.New("schema 4 requires a release-bound installation")
	}
	b := a.installation
	files := b.Bundle
	if e := ValidateInstallationKind(files["platform/development/bootstrap/kind.json"], b.HTTPPort, b.HTTPSPort); e != nil {
		return e
	}
	d := &developmentBundle{files: files, fingerprint: platform.BundleDigest(files), baselineFingerprint: platform.BundleDigest(files), images: b.Images, apps: map[string]Object{}}
	for path, dst := range map[string]*Object{"platform/development/bootstrap/root.json": &d.root, "platform/development/bootstrap/project.json": &d.project, "platform/development/bootstrap/kind.json": &d.kind} {
		if e := strictJSON(files[path], dst); e != nil {
			return e
		}
	}
	for _, path := range []string{"gitops/root/overlays/development/resources.json", "gitops/platform/applications/overlays/development/resources.json", "gitops/workloads/applications/overlays/development/resources.json"} {
		objects, e := platform.DecodeJSONManifests(files[path])
		if e != nil {
			return e
		}
		for _, o := range objects {
			meta, _ := o["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if o["kind"] != "Application" || name == "" || d.apps[name] != nil {
				return errors.New("invalid installation control graph")
			}
			if meta["namespace"] != platformnames.Argo {
				return errors.New("installation Application namespace differs from current product layout")
			}
			spec, _ := o["spec"].(map[string]any)
			source, _ := spec["source"].(map[string]any)
			if source["repoURL"] != a.Config.RepositoryURL || source["targetRevision"] != a.Config.Revision {
				return errors.New("Application differs from deployment binding")
			}
			d.apps[name] = Object(o)
		}
	}
	if d.apps["cilium"] == nil || d.apps["argocd-self"] == nil || d.apps["platform-control"] == nil {
		return errors.New("incomplete installation control graph")
	}
	if a.development != nil && a.development.fingerprint != d.fingerprint {
		return errors.New("installation projection changed during command")
	}
	a.development = d
	return nil
}
func (a *App) verifyInstallationRepository(ctx context.Context, files map[string][]byte) error {
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(a.installation.DeploymentCommit) {
		return errors.New("installation has no confirmed published deployment")
	}
	for path, want := range files {
		if !strings.HasPrefix(path, "gitops/") {
			continue
		}
		got, e := a.run(ctx, "git", "show", a.installation.DeploymentCommit+":"+path)
		if e != nil {
			return e
		}
		if digest(got) != digest(want) {
			return fmt.Errorf("published deployment differs: %s", path)
		}
	}
	// Resolve afresh at every mutation invocation. Product source HEAD is irrelevant.
	a.resolvedCommit = ""
	got, e := a.sourceCommit(ctx)
	if e != nil {
		return e
	}
	if got != a.installation.DeploymentCommit {
		return errors.New("remote deployment changed outside the approved installation")
	}
	return nil
}
func (c Config) fourNodeProfile() bool { return c.Schema == 3 || c.Schema == 4 }

// Initial D1 adoption must surface an exhausted sync, even when a parent is
// still Progressing while waiting for that child. This is a read-only check;
// a terminal error never restores Seed authority or changes GitOps resources.
func (a *App) checkInstallationHandoffFailure(ctx context.Context) error {
	if a.installation == nil {
		return nil // Preserve the frozen schema 1–3 observation contract.
	}
	b, e := a.kube(ctx, nil, "get", "applications", "-n", a.argoNamespace(), "-o", "json")
	if e != nil {
		return e
	}
	var list struct {
		Kind  string
		Items []Live
	}
	if e = decode(b, &list); e != nil {
		return e
	}
	if list.Kind != "List" && list.Kind != "ApplicationList" {
		return errors.New("invalid installation Application list")
	}
	for _, app := range list.Items {
		if app.Metadata.Name == "" || app.Metadata.Namespace != a.argoNamespace() || app.Metadata.UID == "" {
			return errors.New("invalid installation Application identity")
		}
		op := app.Status.OperationState
		if app.Operation == nil && op.SyncResult.Revision == a.installation.DeploymentCommit && (op.Phase == "Failed" || op.Phase == "Error") {
			return fmt.Errorf("GitOps Application %s failed initial handoff at %s; Seed remains denied", app.Metadata.Name, op.SyncResult.Revision)
		}
	}
	return nil
}

// Historical CLI schemas are frozen; only authenticated D1 products use the
// current layout. This is not a user-configurable namespace override.
func (a *App) argoNamespace() string {
	if a.Config.Schema == 4 {
		return platformnames.Argo
	}
	return "argocd"
}
