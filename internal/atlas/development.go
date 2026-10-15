package atlas

import (
	"atlas-refactor/internal/developmentprofile"
	"atlas-refactor/internal/platform"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const developmentSeed = "platform/development/bootstrap/argocd-self-seed.yaml"
const ciliumSeed = "platform/development/bootstrap/cilium-seed.yaml"
const developmentSignal = "gitops/platform/management/argocd-self/overlays/development/signal.json"

type developmentBundle struct {
	files               map[string][]byte
	fingerprint         string
	baselineFingerprint string
	images              []string
	apps                map[string]Object
	root, project, kind Object
}

func (c Config) developmentProfile() bool { return c.Schema == 2 || c.Schema == 3 }

func (a *App) prepareDevelopment() error {
	p, e := platform.Load(a.Root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		return e
	}
	for path, artifact := range p.Lock.Artifacts {
		b, err := readFile(a.Root, path)
		if err != nil {
			return err
		}
		if digest(b) != artifact.SHA256 {
			return fmt.Errorf("development artifact digest mismatch: %s", path)
		}
	}
	if e = p.VerifyArtifacts(); e != nil {
		return e
	}
	if a.Config.GitOpsPath != p.Config.RootPath || a.Config.RepositoryURL != p.Config.RepositoryURL || a.Config.Revision != p.Config.Revision {
		return errors.New("development Bootstrap and GitOps configurations disagree")
	}
	if p.Lock.Images["node"] != a.Lock.NodeImage || p.Lock.Images["argo"] != a.Lock.ArgoImage || p.Lock.Images["redis"] != a.Lock.RedisImage {
		return errors.New("development and base image locks disagree")
	}
	if e = p.ValidateCapabilityActivation(); e != nil {
		return e
	}
	files, e := p.BundleFiles()
	if e != nil {
		return e
	}
	// Runtime reads must obey the same symlink boundary as normal Bootstrap.
	for path, want := range files {
		b, e := readFile(a.Root, path)
		if e != nil {
			return e
		}
		if digest(b) != digest(want) {
			return errors.New("development bundle changed during preparation")
		}
	}
	d := &developmentBundle{files: files, fingerprint: platform.BundleDigest(files), images: platform.ImageList(p.Lock.Images), apps: map[string]Object{}}
	if d.baselineFingerprint, e = a.validateDevelopmentBaseline(files, a.Config.Schema); e != nil {
		return e
	}
	for path, dst := range map[string]*Object{"platform/development/bootstrap/root.json": &d.root, "platform/development/bootstrap/project.json": &d.project, "platform/development/bootstrap/kind.json": &d.kind} {
		if e = decode(files[path], dst); e != nil {
			return e
		}
	}
	if e = validateDevelopmentKindFor(a.Config.Revision, files["platform/development/bootstrap/kind.json"]); e != nil {
		return e
	}
	for _, path := range []string{"gitops/root/overlays/development/resources.json", "gitops/platform/applications/overlays/development/resources.json", "gitops/workloads/applications/overlays/development/resources.json"} {
		objects, e := platform.DecodeJSONManifests(files[path])
		if e != nil {
			return e
		}
		for _, obj := range objects {
			o := Object(obj)
			meta, _ := o["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if o["kind"] != "Application" || name == "" || d.apps[name] != nil {
				return errors.New("invalid development Application inventory")
			}
			d.apps[name] = o
		}
	}
	if len(d.apps) != 4+len(p.Config.Components) || d.apps["cilium"] == nil || d.apps["argocd-self"] == nil {
		return errors.New("incomplete development control graph")
	}
	if a.development != nil && a.development.fingerprint != d.fingerprint {
		return errors.New("development bundle changed during command")
	}
	a.development = d
	return nil
}
func (a *App) lockedImages() []string {
	if a.development != nil {
		return a.development.images
	}
	return []string{a.Lock.NodeImage, a.Lock.ArgoImage, a.Lock.RedisImage}
}
func (a *App) seedFile() string {
	if a.development != nil {
		return developmentSeed
	}
	return "bootstrap/seed.yaml"
}
func (a *App) selfApplication() Object {
	if a.development != nil {
		return a.development.apps["argocd-self"]
	}
	return a.application("argocd-self", "platform-project", a.Config.GitOpsPath+"/platform/argocd", "0")
}
func (a *App) developmentRender() map[string][]byte {
	files := map[string][]byte{}
	for path, b := range a.development.files {
		files[path] = b
	}
	signal := configMap(a.argoNamespace(), "atlas-refactor-adoption-signal", map[string]string{"fingerprint": a.fingerprint()})
	signal["metadata"].(Object)["annotations"] = Object{"argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}
	files[developmentSignal] = jsonBytes(signal)
	return files
}
func (a *App) prepareCilium(ctx context.Context, files map[string][]byte) error {
	if e := a.verifySubstrate(ctx, false); e != nil {
		return e
	}
	for _, im := range a.lockedImages() {
		if im != a.Lock.NodeImage {
			if e := a.loadNodeImage(ctx, im); e != nil {
				return e
			}
		}
	}
	if _, e := a.kube(ctx, files[ciliumSeed], "apply", "--server-side", "--field-manager=atlas-refactor-bootstrap", "-f", "-"); e != nil {
		return fmt.Errorf("Cilium Seed: %w", e)
	}
	for _, target := range []string{"daemonset/cilium", "deployment/cilium-operator"} {
		if _, e := a.kube(ctx, nil, "rollout", "status", target, "-n", "kube-system", "--timeout=180s"); e != nil {
			return e
		}
	}
	if _, e := a.kube(ctx, nil, "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=180s"); e != nil {
		return e
	}
	return a.VerifyNodes(ctx)
}
func (a *App) developmentHandoffComplete(ctx context.Context, root, self, signal *Live, adopted bool) (bool, error) {
	if adopted && a.Config.fourNodeProfile() {
		return a.developmentAuthorityIntact(ctx, self, signal)
	}
	return a.developmentRolloutComplete(ctx, root, self, signal, adopted)
}

// VerifyDevelopmentRollout preserves atlas-dev verify's explicit rollout check.
// Unlike OT-1 it has no reviewed multi-revision plan, so it stays exact-current.
// It is read-only and is never called by post-Receipt status or normal apply.
func (a *App) VerifyDevelopmentRollout(ctx context.Context) error {
	if e := a.VerifyArtifacts(); e != nil {
		return e
	}
	o, e := a.inspect(ctx)
	if e != nil {
		return e
	}
	if a.development == nil || o.report.State != Adopted {
		return errors.New("development rollout requires intact durable handoff")
	}
	complete, e := a.developmentRolloutComplete(ctx, o.root, o.self, o.signal, true)
	if e != nil {
		return e
	}
	if !complete {
		return errors.New("development rollout is not complete")
	}
	return nil
}

func (a *App) developmentRolloutComplete(ctx context.Context, root, self, signal *Live, adopted bool) (bool, error) {
	if signal == nil || !ready(root) || !ready(self) {
		return false, nil
	}
	commit, e := a.sourceCommit(ctx)
	if e != nil {
		return false, e
	}
	if root.Status.Sync.Revision != commit {
		return false, nil
	}
	liveApps := map[string]*Live{"argocd-self": self}
	names := make([]string, 0, len(a.development.apps))
	for name := range a.development.apps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		live := liveApps[name]
		if live == nil {
			live, e = a.get(ctx, "application", a.argoNamespace(), name)
			if e != nil {
				return false, e
			}
		}
		if !specMatches(live, a.development.apps[name]) || !ready(live) || live.Status.Sync.Revision != commit {
			return false, nil
		}
		liveApps[name] = live
	}
	for name, path := range map[string]string{"argocd-self": developmentSeed, "cilium": ciliumSeed} {
		proofCommit := commit
		if adopted && a.Config.fourNodeProfile() {
			proofCommit = liveApps[name].Status.OperationState.SyncResult.Revision
			if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(proofCommit) {
				return false, nil
			}
		}
		owned, e := a.seedPayloadOwned(ctx, liveApps[name], proofCommit, a.development.files[path])
		if e != nil || !owned {
			return false, e
		}
	}
	return true, nil
}

// Called only after inspect validates the immutable Receipt/Latch/Identity and
// Root/self/Signal bindings. It checks continuing Seed ownership, not Git rollout.
func (a *App) developmentAuthorityIntact(ctx context.Context, self, signal *Live) (bool, error) {
	if self == nil || signal == nil {
		return false, nil
	}
	cilium, e := a.get(ctx, "application", a.argoNamespace(), "cilium")
	if e != nil || !specMatches(cilium, a.development.apps["cilium"]) {
		return false, e
	}
	for name, live := range map[string]*Live{"argocd-self": self, "cilium": cilium} {
		path := developmentSeed
		if name == "cilium" {
			path = ciliumSeed
		}
		owned, e := a.seedPayloadOwnership(ctx, live, "", a.development.files[path], false)
		if e != nil || !owned {
			return false, e
		}
	}
	return true, nil
}

func (a *App) developmentIdentity(d map[string]string) {
	d["schema"] = "atlas-refactor/identity/v2-development"
	d["substrateProfile"] = "kind-cilium-ipv4-development/v1"
	if a.Config.fourNodeProfile() {
		d["schema"] = "atlas-refactor/identity/v3-development"
		d["substrateProfile"] = "kind-cilium-ipv4-four-node/v1"
	}
	if a.Config.Revision == developmentprofile.OT1Revision {
		d["substrateProfile"] = "kind-cilium-ipv4-four-node-ot1/v1"
	}
	d["platformSHA256"] = a.development.baselineFingerprint
	if a.installation != nil {
		d["schema"] = "atlas-refactor/identity/v4-installation"
		d["productSHA256"] = a.installation.ProductSHA256
		d["binarySHA256"] = a.installation.BinarySHA256
		d["installID"] = a.installation.InstallID
	}
}
func (a *App) initialWait() string {
	if a.development != nil {
		return "0s"
	}
	return "120s"
}
func (a *App) artifactPath(path string) string {
	if a.development != nil {
		return path
	}
	return strings.TrimSuffix(a.Config.GitOpsPath, "/") + "/" + path
}

// Keep runtime substrate exposure within the reviewed four-node local profile.
func validateDevelopmentKind(data []byte) error {
	return validateDevelopmentKindFor(developmentprofile.DevelopmentRevision, data)
}

func validateDevelopmentKindFor(revision string, data []byte) error {
	profile, err := developmentprofile.Lookup(revision)
	if err != nil {
		return err
	}
	return ValidateInstallationKind(data, profile.HTTPPort, profile.HTTPSPort)
}

// ValidateInstallationKind restricts D1 to the same reviewed four-node topology;
// only the two unprivileged, loopback host ports are selected per installation.
func ValidateInstallationKind(data []byte, httpPort, httpsPort int) error {
	if httpPort < 1024 || httpPort > 65535 || httpsPort < 1024 || httpsPort > 65535 || httpPort == httpsPort {
		return errors.New("invalid installation ingress ports")
	}
	const expected = `{"apiVersion":"kind.x-k8s.io/v1alpha4","kind":"Cluster","networking":{"ipFamily":"ipv4","apiServerAddress":"127.0.0.1","disableDefaultCNI":true,"kubeProxyMode":"iptables"},"nodes":[{"role":"control-plane"},{"role":"worker","labels":{"node-role.local/gateway":"true"},"extraPortMappings":[{"containerPort":30080,"hostPort":8080,"listenAddress":"127.0.0.1","protocol":"TCP"},{"containerPort":30443,"hostPort":8443,"listenAddress":"127.0.0.1","protocol":"TCP"}]},{"role":"worker","labels":{"node-role.local/compute":"true"}},{"role":"worker","labels":{"node-role.local/data":"true","topology.kubernetes.io/zone":"data-zone-1"},"kubeadmConfigPatches":["apiVersion: kubeadm.k8s.io/v1beta4\nkind: JoinConfiguration\nnodeRegistration:\n  taints:\n    - key: node-role.local/data\n      value: \"true\"\n      effect: NoSchedule\n"]}]}`
	var got, want Object
	if err := strictJSON(data, &got); err != nil {
		return err
	}
	if err := strictJSON([]byte(expected), &want); err != nil {
		return err
	}
	mappings := want["nodes"].([]any)[1].(map[string]any)["extraPortMappings"].([]any)
	mappings[0].(map[string]any)["hostPort"] = float64(httpPort)
	mappings[1].(map[string]any)["hostPort"] = float64(httpsPort)
	if !reflect.DeepEqual(got, want) {
		return errors.New("development Kind configuration exceeds the reviewed local profile")
	}
	return nil
}

// The snapshot preserves the first instantiation identity. Current GitOps leaf
// payloads may evolve, but the authority-bearing Bootstrap contract must match.
func (a *App) validateDevelopmentBaseline(files map[string][]byte, schema int) (string, error) {
	var baseline struct {
		Schema       int               `json:"schema"`
		Commit       string            `json:"commit"`
		Projection   string            `json:"projection,omitempty"`
		BundleHashes map[string]string `json:"bundleHashes"`
	}
	if err := strictJSON(files["platform/development/bootstrap/baseline.json"], &baseline); err != nil {
		return "", err
	}
	if baseline.Schema != 1 || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(baseline.Commit) {
		return "", errors.New("invalid development baseline")
	}
	for path, hash := range baseline.BundleHashes {
		if path == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
			return "", errors.New("invalid baseline input digest")
		}
	}
	bound := []string{
		"platform/development/config.json", "platform/development/versions.lock.json",
		"platform/development/health/ready.lua", "platform/development/values/cilium.json",
		"platform/development/bootstrap/kind.json", "platform/development/bootstrap/project.json",
		"platform/development/bootstrap/root.json", developmentSeed, ciliumSeed,
		"gitops/root/overlays/development/kustomization.yaml", "gitops/root/overlays/development/resources.json",
		"gitops/platform/applications/overlays/development/kustomization.yaml", "gitops/platform/applications/overlays/development/resources.json",
		"gitops/workloads/applications/overlays/development/kustomization.yaml", "gitops/workloads/applications/overlays/development/resources.json",
		"gitops/platform/management/projects/overlays/development/kustomization.yaml", "gitops/platform/management/projects/overlays/development/resources.json",
		"gitops/platform/management/argocd-self/overlays/development/kustomization.yaml",
		"gitops/platform/networking/cilium/overlays/development/kustomization.yaml",
	}
	if schema == 3 {
		snapshot := files["platform/development/bootstrap/baseline-v3.json"]
		profile, err := developmentprofile.Lookup(a.Config.Revision)
		if err != nil {
			return "", err
		}
		expected := profile.SnapshotSHA256
		if a.fixtureSnapshotDigest != "" {
			expected = a.fixtureSnapshotDigest
		}
		if digest(snapshot) != expected {
			return "", errors.New("four-node instantiation snapshot changed")
		}
		if err := strictJSON(snapshot, &baseline); err != nil {
			return "", err
		}
		if a.Config.Revision == developmentprofile.OT1Revision && (baseline.Commit != developmentprofile.OT1SourceCommit || baseline.Projection != developmentprofile.OT1Projection) {
			return "", errors.New("OT-1 instantiation provenance changed")
		}
		contract := map[string][]byte{}
		for _, path := range bound {
			b, ok := files[path]
			if !ok {
				return "", fmt.Errorf("missing Bootstrap contract input: %s", path)
			}

			// Only the Tier-1 catalog and its least-privilege project projection can
			// evolve. prepareDevelopment validates both against the declared catalog.
			if path == "gitops/platform/applications/overlays/development/resources.json" {
				b = files["platform/development/capabilities/core-applications.json"]
			}
			if path == "gitops/platform/management/projects/overlays/development/resources.json" {
				b = files["platform/development/capabilities/core-projects.json"]
			}
			if digest(b) != baseline.BundleHashes[path] {
				return "", fmt.Errorf("frozen instantiation contract changed: %s", path)
			}
			contract[path] = b
		}
		return platform.BundleDigest(contract), nil
	}
	for _, path := range bound {
		b, ok := files[path]
		if !ok || digest(b) != baseline.BundleHashes[path] {
			return "", fmt.Errorf("Bootstrap contract differs from frozen baseline: %s", path)
		}
	}
	b, err := json.Marshal(baseline.BundleHashes)
	if err != nil {
		return "", err
	}
	return digest(b), nil
}
