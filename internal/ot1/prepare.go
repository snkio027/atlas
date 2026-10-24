// Package ot1 implements only the reviewed, exact foundation rehearsal. It is
// not a general migration API and cannot target dev02 or a caller-selected set.
package ot1

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const OldLayoutCommit = "b618dea24b7c46cd36fd11a568a72c9a88f2097a"
const NewLayoutCommit = "b5d0562381f5b3989578d62e2677364d8c73710d"
const CatalogPath = "platform/development/capabilities/catalog.json"
const ApplicationsPath = "gitops/platform/applications/overlays/development/resources.json"
const ProjectsPath = "gitops/platform/management/projects/overlays/development/resources.json"
const CredentialInput = "platform/development/capabilities/resources/platform-credentials.json"
const CredentialOutput = "gitops/platform/management/platform-credentials/overlays/development/resources.json"
const Source = "capability-foundation"

var Targets = []string{"secrets-foundation", "observability-foundation", "storage-foundation"}

type Preparation struct {
	Schema            int               `json:"schema"`
	Target            string            `json:"target"`
	SourceCommit      string            `json:"sourceCommit"`
	Projection        string            `json:"projection"`
	SnapshotSHA256    string            `json:"snapshotSHA256"`
	Commit            string            `json:"commit"`
	RenderHashes      map[string]string `json:"renderHashes"`
	ClusterOperations int               `json:"clusterOperations"`
	Credentials       string            `json:"credentials"`
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "SYSTEMROOT"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_DATE=2026-09-27T00:00:00Z", "GIT_COMMITTER_DATE=2026-09-27T00:00:00Z")
	out, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("Git %s failed; subprocess output suppressed", args[0])
	}
	return out, nil
}
func write(root, path string, b []byte) error {
	return atlas.WriteFiles(root, ".", map[string][]byte{path: b})
}
func jsonFile(root, path string, v any) error { return write(root, path, observation.Bytes(v)) }
func readObject(root, path string) (observation.Object, error) {
	b, e := os.ReadFile(filepath.Join(root, path))
	if e != nil {
		return nil, e
	}
	var o observation.Object
	e = observation.Decode(b, &o, false)
	return o, e
}
func sourceFile(ctx context.Context, root, sha, path string) ([]byte, error) {
	return git(ctx, root, "show", sha+":"+path)
}

// PrepareRepository creates a private local clone from fixed historical inputs.
// It never fetches, pushes, downloads, creates credentials or calls a cluster.
// An existing destination is a hard error; an interrupted preparation is kept.
func PrepareRepository(ctx context.Context, sourceRoot, destination string, tools platform.Tools) (Preparation, error) {
	result := Preparation{Schema: 1, Target: developmentprofile.OT1Cluster, SourceCommit: developmentprofile.OT1SourceCommit, Projection: developmentprofile.OT1Projection, SnapshotSHA256: developmentprofile.OT1SnapshotSHA256, ClusterOperations: 0, Credentials: "not-generated; private key and ciphertext activation are separate approved preparation", RenderHashes: map[string]string{}}
	abs, e := filepath.Abs(destination)
	if e != nil {
		return result, e
	}
	sourceRoot, e = filepath.Abs(sourceRoot)
	if e != nil {
		return result, e
	}
	if _, e = os.Lstat(abs); !os.IsNotExist(e) {
		return result, errors.New("OT-1 preparation destination already exists; preserve it and choose a new path")
	}
	if e = observation.PrivateDirectory(filepath.Dir(abs)); e != nil {
		return result, e
	}
	if _, e = git(ctx, sourceRoot, "clone", "--no-hardlinks", "--no-checkout", sourceRoot, abs); e != nil {
		return result, e
	}
	if e = os.Chmod(abs, 0700); e != nil {
		return result, e
	}
	if _, e = git(ctx, abs, "checkout", "--detach", developmentprofile.OT1SourceCommit); e != nil {
		return result, e
	}
	if _, e = git(ctx, abs, "switch", "-c", developmentprofile.OT1Revision); e != nil {
		return result, e
	}
	if _, e = git(ctx, abs, "remote", "set-url", "origin", developmentprofile.Repository); e != nil {
		return result, e
	}
	baselineBytes, e := os.ReadFile(filepath.Join(sourceRoot, "experiments/foundation-ownership/profile-baseline.json"))
	if e != nil {
		return result, e
	}
	if observation.SHA(baselineBytes) != developmentprofile.OT1SnapshotSHA256 {
		return result, errors.New("OT-1 snapshot differs from compiled profile")
	}
	var snapshot struct {
		Schema       int               `json:"schema"`
		Commit       string            `json:"commit"`
		Projection   string            `json:"projection"`
		BundleHashes map[string]string `json:"bundleHashes"`
	}
	if e = observation.Decode(baselineBytes, &snapshot, true); e != nil {
		return result, e
	}
	// Project only source ref and loopback host ports from immutable core inputs.
	for path, want := range snapshot.BundleHashes {
		from := path
		if path == ApplicationsPath {
			from = "platform/development/capabilities/core-applications.json"
		}
		if path == ProjectsPath {
			from = "platform/development/capabilities/core-projects.json"
		}
		b, e := os.ReadFile(filepath.Join(abs, from))
		if e != nil {
			return result, e
		}
		b = bytes.ReplaceAll(b, []byte(developmentprofile.DevelopmentRevision), []byte(developmentprofile.OT1Revision))
		if path == "platform/development/bootstrap/kind.json" {
			b = bytes.ReplaceAll(b, []byte(`"hostPort": 8080`), []byte(`"hostPort": 18080`))
			b = bytes.ReplaceAll(b, []byte(`"hostPort": 8443`), []byte(`"hostPort": 18443`))
		}
		if observation.SHA(b) != want {
			return result, fmt.Errorf("OT-1 frozen projection mismatch: %s", path)
		}
		if e = write(abs, from, b); e != nil {
			return result, e
		}
		if from != path {
			if e = write(abs, path, b); e != nil {
				return result, e
			}
		}
	}
	if e = write(abs, "platform/development/bootstrap/baseline-v3.json", baselineBytes); e != nil {
		return result, e
	}
	config := atlas.Config{Schema: 3, Cluster: developmentprofile.OT1Cluster, RepositoryURL: developmentprofile.Repository, Revision: developmentprofile.OT1Revision, GitOpsPath: developmentprofile.RootPath, DockerContext: "orbstack", TimeoutSeconds: 1800}
	if e = config.Validate(); e != nil {
		return result, e
	}
	if e = jsonFile(abs, "profiles/ot1.json", config); e != nil {
		return result, e
	}
	// Keep the ordinary default profile visible, but it cannot validate against
	// this clone's different source/snapshot. All instructions use --config ot1.
	legacy, e := sourceFile(ctx, sourceRoot, OldLayoutCommit, CatalogPath)
	if e != nil {
		return result, e
	}
	var catalog platform.CapabilityCatalog
	if e = observation.Decode(legacy, &catalog, true); e != nil {
		return result, e
	}
	catalog.Schema = 2
	catalog.PermissionDomains = map[string]string{"legacy": "platform-project"}
	for name, component := range catalog.Components {
		component.PermissionDomain = "legacy"
		catalog.Components[name] = component
	}
	if e = jsonFile(abs, CatalogPath, catalog); e != nil {
		return result, e
	}
	for _, path := range []string{"platform/development/capabilities/resources/capability-foundation.json", "gitops/platform/foundation/capabilities/overlays/development/resources.json", "gitops/platform/foundation/capabilities/overlays/development/kustomization.yaml"} {
		b, e := sourceFile(ctx, sourceRoot, OldLayoutCommit, path)
		if e != nil {
			return result, e
		}
		if e = write(abs, path, b); e != nil {
			return result, e
		}
	}
	empty := observation.Object{"apiVersion": "v1", "kind": "List", "items": []any{}}
	for _, path := range []string{CredentialInput, CredentialOutput} {
		if e = jsonFile(abs, path, empty); e != nil {
			return result, e
		}
	}
	if e = jsonFile(abs, "platform/development/capabilities/enabled.json", platform.EnabledCapabilities{Schema: 1, Capabilities: []string{}}); e != nil {
		return result, e
	}
	web, e := readObject(abs, "gitops/workloads/web-smoke/overlays/development/resources.json")
	if e != nil {
		return result, e
	}
	changed := 0
	for _, raw := range observation.Slice(web["items"]) {
		o := observation.Map(raw)
		if observation.String(o["kind"]) != "HTTPRoute" {
			continue
		}
		for _, rule := range observation.Slice(observation.At(o, "spec", "rules")) {
			for _, filter := range observation.Slice(observation.Map(rule)["filters"]) {
				f := observation.Map(filter)
				redirect := observation.Map(f["requestRedirect"])
				if observation.String(f["type"]) == "RequestRedirect" {
					redirect["port"] = 18443
					changed++
				}
			}
		}
	}
	if changed != 1 {
		return result, errors.New("expected one HTTP redirect in isolated profile")
	}
	if e = jsonFile(abs, "gitops/workloads/web-smoke/overlays/development/resources.json", web); e != nil {
		return result, e
	}
	p, e := platform.Load(abs, tools)
	if e != nil {
		return result, e
	}
	files, e := p.Render(ctx)
	if e != nil {
		return result, e
	}
	if e = p.Write(files); e != nil {
		return result, e
	}
	activation, e := p.CapabilityActivation(nil)
	if e != nil {
		return result, e
	}
	if e = p.Write(activation); e != nil {
		return result, e
	}
	_, lock, e := atlas.Load(abs, "profiles/ot1.json")
	if e != nil {
		return result, e
	}
	app := &atlas.App{Root: abs, Config: config, Lock: lock}
	rendered, e := app.Render(ctx)
	if e != nil {
		return result, e
	}
	if e = atlas.WriteFiles(abs, ".", rendered); e != nil {
		return result, e
	}
	if _, e = p.Check(ctx); e != nil {
		return result, e
	}
	for path, b := range rendered {
		result.RenderHashes[path] = observation.SHA(b)
	}
	// Strict manual Application definitions are Git-owned inputs, outside the
	// platform parent projection until each ceremony stage explicitly uses them.
	for _, owner := range append([]string{Source}, Targets...) {
		app, err := Application(owner, true)
		if err != nil {
			return result, err
		}
		if err = jsonFile(abs, "experiments/foundation-ownership/applications/"+owner+".json", app); err != nil {
			return result, err
		}
	}
	// Only the local artifact branch is committed, with deterministic timestamps.
	if _, e = git(ctx, abs, "add", "--all"); e != nil {
		return result, e
	}
	if _, e = git(ctx, abs, "-c", "user.name=Atlas OT-1 preparation", "-c", "user.email=atlas-ot1@invalid.local", "commit", "-m", "Prepare isolated OT-1 pre-split core desired state"); e != nil {
		return result, e
	}
	commit, e := git(ctx, abs, "rev-parse", "HEAD")
	if e != nil {
		return result, e
	}
	result.Commit = strings.TrimSpace(string(commit))
	if e = observation.CreatePrivate(filepath.Join(abs, ".state", "ot1-preparation.json"), observation.Bytes(result)); e != nil {
		return result, e
	}
	return result, nil
}
