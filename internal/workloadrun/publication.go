package workloadrun

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/installation"
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"atlas-refactor/internal/workload"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type Plan struct {
	Schema               int               `json:"schema"`
	Cluster              string            `json:"cluster"`
	ClusterUID           string            `json:"clusterUID"`
	InstallID            string            `json:"installID"`
	Repository           string            `json:"repository"`
	Branch               string            `json:"branch"`
	Parent               string            `json:"parent"`
	BaseCommit           string            `json:"baseCommit"`
	ProductSHA256        string            `json:"productSHA256"`
	CompilerSHA256       string            `json:"compilerSHA256"`
	ConfigSHA256         string            `json:"configSHA256"`
	IntentSHA256         string            `json:"intentSHA256"`
	PhaseSHA256          map[string]string `json:"phaseSHA256"`
	CertificateSHA256    string            `json:"certificateSHA256"`
	Project              string            `json:"project"`
	CredentialTargets    []string          `json:"credentialTargets"`
	Phases               []string          `json:"phases"`
	ImplementationCommit string            `json:"implementationCommit"`
	GoVersion            string            `json:"goVersion"`
	SourceDirty          bool              `json:"sourceDirty"`
}

func (w *Workflow) Plan(ctx context.Context) (Plan, error) {
	p := Plan{Schema: 2, Cluster: w.Install.Config.Cluster, ClusterUID: w.Install.Record.ClusterUID, InstallID: w.Install.Record.InstallID, Repository: w.Context.Repository, Branch: w.Context.Branch, BaseCommit: w.Install.Record.FullCommit, ProductSHA256: w.Install.ProductDigest, CompilerSHA256: w.BinarySHA256, ConfigSHA256: workload.Digest(workload.JSON(w.Config)), IntentSHA256: workload.Digest(workload.JSON(w.Model.Intent)), CertificateSHA256: w.Install.Record.CertificateSHA256, Project: w.Model.Intent.Project.Name, CredentialTargets: []string{}, Phases: publicationPhases(true)}
	if !w.application() && (len(w.Model.Intent.Bindings) == 0 || len(w.Model.Intent.Workloads) <= len(w.Model.Intent.Bindings)) {
		return p, errors.New("this S2 acceptance runner requires both bound and unbound WebServices")
	}
	if e := w.checkPublicationAccess(ctx); e != nil {
		return p, e
	}
	p.ImplementationCommit = w.BuildSource
	p.GoVersion = w.BuildGoVersion
	p.SourceDirty = w.BuildDirty
	parent, e := w.remote(ctx)
	if e != nil {
		return p, e
	}
	p.Parent = parent
	if _, _, e := w.imageArchive(); e != nil {
		return p, e
	}
	current, e := w.ReadTree(ctx, parent)
	if e != nil {
		return p, e
	}
	if parent != w.Install.Record.FullCommit {
		if e = w.validatePredecessor(current); e != nil {
			return p, e
		}
		var inv workload.Inventory
		if e = installation.Decode(current[workload.InventoryPath], &inv); e != nil {
			return p, e
		}
		if inv.Phase != "consumer" {
			return p, errors.New("an incomplete publication cannot become a new plan")
		}
		p.Phases = []string{"consumer"}
	}
	p.PhaseSHA256, e = w.phaseDigests()
	if e != nil {
		return p, e
	}
	if len(w.Model.Intent.Bindings) > 0 {
		p.CredentialTargets = append(p.CredentialTargets, platformnames.ObjectStorage+"/seaweedfs-auth")
	}
	for _, b := range w.Model.Intent.Bindings {
		p.CredentialTargets = append(p.CredentialTargets, b.Namespace()+"/"+b.Secret())
	}
	return p, nil
}
func (w *Workflow) ReadPlan() (Plan, error) {
	if w.observationPlan != nil {
		return *w.observationPlan, nil
	}
	var p Plan
	b, e := regular(filepath.Join(w.Config.StateDirectory, "plan.json"), true)
	if e == nil {
		e = workload.StrictDecode(b, &p)
	}
	return p, e
}
func (w *Workflow) approve(p Plan, digest string) error {
	if w.observationPlan != nil {
		return errors.New("recorded observation view has no mutation authority")
	}
	if p.SourceDirty || w.BuildDirty || !fullSHA.MatchString(p.ImplementationCommit) || p.ImplementationCommit != w.BuildSource || p.GoVersion != w.BuildGoVersion {
		return errors.New("execution requires a clean, source-bound compiled binary")
	}
	if digest == "" || digest != workload.Digest(workload.JSON(p)) || p.Schema != 2 || p.CompilerSHA256 != w.BinarySHA256 || p.ProductSHA256 != w.Install.ProductDigest || p.ClusterUID != w.Install.Record.ClusterUID || p.InstallID != w.Install.Record.InstallID || p.Repository != w.Context.Repository || p.Branch != w.Context.Branch || p.BaseCommit != w.Install.Record.FullCommit || p.ConfigSHA256 != workload.Digest(workload.JSON(w.Config)) || p.IntentSHA256 != workload.Digest(workload.JSON(w.Model.Intent)) || p.CertificateSHA256 != w.Install.Record.CertificateSHA256 {
		return errors.New("exact approved S2 plan/input binding required")
	}
	if !slices.Equal(p.Phases, publicationPhases(p.Parent == p.BaseCommit)) || p.Project != w.Model.Intent.Project.Name || !fullSHA.MatchString(p.Parent) {
		return errors.New("invalid fixed publication sequence")
	}
	digests, e := w.phaseDigests()
	if e != nil {
		return e
	}
	if !bytes.Equal(workload.JSON(digests), workload.JSON(p.PhaseSHA256)) {
		return errors.New("compiled publication prerequisites changed")
	}
	return nil
}
func (w *Workflow) SavePlan(p Plan) error {
	return save(filepath.Join(w.Config.StateDirectory, "plan.json"), workload.JSON(p), false)
}
func (w *Workflow) validatePredecessor(files Files) error {
	var inv workload.Inventory
	if e := installation.Decode(files[workload.InventoryPath], &inv); e != nil {
		return e
	}
	if inv.Schema != 1 || inv.ProductSHA256 != w.Install.ProductDigest || inv.BaseSHA256 != platform.BundleDigest(w.Context.Base) {
		return errors.New("unknown S2 predecessor")
	}
	for name, digest := range inv.Files {
		if workload.Digest(files[name]) != digest {
			return fmt.Errorf("prior generated/authored file drifted: %s", name)
		}
	}
	in := workload.Intent{Workloads: []workload.Workload{}, Bindings: []workload.Binding{}}
	for name, b := range files {
		if strings.HasPrefix(name, "platform/projects/") || strings.HasPrefix(name, "platform/workloads/") || strings.HasPrefix(name, "platform/bindings/") {
			if !strings.HasSuffix(name, ".yaml") {
				return errors.New("predecessor declarations must use YAML")
			}
			var err error
			b, err = authored.JSON(b)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		switch {
		case strings.HasPrefix(name, "platform/projects/"):
			if in.Project.Name != "" {
				return errors.New("multiple predecessor projects")
			}
			if e := workload.StrictDecode(b, &in.Project); e != nil {
				return e
			}
		case strings.HasPrefix(name, "platform/workloads/"):
			var v workload.Workload
			if e := workload.StrictDecode(b, &v); e != nil {
				return e
			}
			in.Workloads = append(in.Workloads, v)
		case strings.HasPrefix(name, "platform/bindings/"):
			var v workload.Binding
			if e := workload.StrictDecode(b, &v); e != nil {
				return e
			}
			in.Bindings = append(in.Bindings, v)
		}
	}
	old, e := workload.Resolve(in, true)
	if e != nil {
		return e
	}
	if old.Intent.Project.Name != w.Model.Intent.Project.Name {
		return fmt.Errorf("deployment branch already owns Project %q; use its existing workspace or a dedicated completed D1 instance", old.Intent.Project.Name)
	}
	// Consumer-only updates have no prerequisite Gate before new TLS listeners.
	// Keep additive authoring separate from the currently supported publication
	// protocol; existing Workload updates preserve their Certificate identity.
	if len(old.Intent.Workloads) != len(w.Model.Intent.Workloads) {
		return errors.New("consumer-only update cannot add or remove Workloads; new TLS dependencies require prerequisite publication")
	}
	return workload.ValidateUpdate(old, w.Model)
}

type Publication struct {
	Schema     int    `json:"schema"`
	PlanSHA256 string `json:"planSHA256"`
	Phase      string `json:"phase"`
	Parent     string `json:"parent"`
	Commit     string `json:"commit"`
	TreeSHA256 string `json:"treeSHA256"`
}

func (w *Workflow) publicationPath(p Plan, phase string) string {
	return filepath.Join(w.Config.StateDirectory, "authority", workload.Digest(workload.JSON(p)), phase+".json")
}
func (w *Workflow) Publish(ctx context.Context, p Plan, approval, phase string) (Publication, error) {
	var receipt Publication
	if e := w.approve(p, approval); e != nil {
		return receipt, e
	}
	if e := w.bind(ctx); e != nil {
		return receipt, e
	}
	allowed := false
	for _, s := range p.Phases {
		allowed = allowed || s == phase
	}
	if !allowed {
		return receipt, errors.New("phase not in approved plan")
	}
	if e := w.publicationWritable(p, phase); e != nil {
		return receipt, e
	}
	result, e := w.Compile(phase)
	if e != nil {
		return receipt, e
	}
	current, e := w.remote(ctx)
	if e != nil {
		return receipt, e
	}
	// A completed phase is an idempotent read, not another publication.
	if b, e := regular(w.publicationPath(p, phase), true); e == nil {
		if e = workload.StrictDecode(b, &receipt); e != nil {
			return receipt, e
		}
		if receipt.PlanSHA256 != approval || receipt.TreeSHA256 != platform.BundleDigest(result.Files) || receipt.Commit != current {
			return receipt, errors.New("published phase/remote drift")
		}
		return receipt, nil
	} else if !os.IsNotExist(e) {
		return receipt, e
	}
	// A receipt records a push, not readiness. Even standalone publish must
	// re-observe its predecessor before exposing any dependent source tree.
	prior, e := w.precedingPublication(p, phase)
	if e != nil {
		return receipt, e
	}
	expectedParent := p.Parent
	if prior != nil {
		expectedParent = prior.Commit
		r, e := w.Compile(prior.Phase)
		if e != nil {
			return receipt, e
		}
		// A closing-read race is Pending, just as during the post-publication
		// Gate. Keep one bounded read session (including UID history); never
		// retry the publication, intent creation or push around this wait.
		report, err := w.ObserveFor(ctx, r, expectedParent, 15*time.Minute)
		if err != nil {
			return receipt, fmt.Errorf("%s gate: %w", prior.Phase, err)
		}
		if err = w.retainGate(p, report); err != nil {
			return receipt, err
		}
	}
	if current != expectedParent {
		return receipt, errors.New("deployment branch moved outside approved sequence")
	}
	parentFiles, e := w.ReadTree(ctx, current)
	if e != nil {
		return receipt, e
	}
	if current != w.Install.Record.FullCommit {
		if e = w.validatePredecessor(parentFiles); e != nil {
			return receipt, e
		}
	}
	// Preserve every unrelated path from the complete current parent. Only the
	// compiler's declared delta can replace existing bytes.
	merged := Files{}
	for k, v := range parentFiles {
		merged[k] = v
	}
	for k, v := range result.Files {
		if bytes.Equal(v, w.Context.Base[k]) {
			if !bytes.Equal(parentFiles[k], v) {
				return receipt, fmt.Errorf("base file drift: %s", k)
			}
			continue
		}
		if old, exists := parentFiles[k]; exists && current == w.Install.Record.FullCommit && !bytes.Equal(old, w.Context.Base[k]) {
			return receipt, errors.New("new output collides with existing deployment path")
		}
		merged[k] = v
	}
	if sameFiles(parentFiles, merged) {
		receipt = Publication{1, approval, phase, current, current, platform.BundleDigest(result.Files)}
		return receipt, save(w.publicationPath(p, phase), workload.JSON(receipt), true)
	}
	if e = w.checkPublicationAccess(ctx); e != nil {
		return receipt, e
	}
	commit, e := w.commit(ctx, merged, current, "S2 "+phase+" for "+p.Project)
	if e != nil {
		return receipt, e
	}
	receipt = Publication{1, approval, phase, current, commit, platform.BundleDigest(result.Files)}
	// Retain request intent before the only remote mutation. Ambiguous outcomes
	// require inspection; they are never blindly republished or force recovered.
	if e = save(w.publicationPath(p, phase)+".intent", workload.JSON(receipt), true); e != nil {
		return receipt, e
	}
	if _, e = w.git(ctx, nil, "push", "--force-with-lease=refs/heads/"+p.Branch+":"+current, p.Repository, commit+":refs/heads/"+p.Branch); e != nil {
		return receipt, fmt.Errorf("%s Git push failed; intent retained for outcome inspection: %w", phase, e)
	}
	observed, e := w.remote(ctx)
	if e != nil || observed != commit {
		return receipt, errors.New("publication outcome unknown; preserve intent")
	}
	return receipt, save(w.publicationPath(p, phase), workload.JSON(receipt), true)
}

// publicationPhases deliberately describes one fixed protocol, not a resume
// graph. Updates keep the existing Project/namespace (ValidateUpdate).
func publicationPhases(fresh bool) []string {
	if fresh {
		return []string{"permissions", "project", "infrastructure", "consumer"}
	}
	return []string{"consumer"}
}

func (w *Workflow) phaseDigests() (map[string]string, error) {
	digests := map[string]string{}
	for _, phase := range []string{"permissions", "project", "infrastructure"} {
		r, e := w.Compile(phase)
		if e != nil {
			return nil, e
		}
		digests[phase] = platform.BundleDigest(r.Files)
	}
	return digests, nil
}

func (w *Workflow) publicationWritable(p Plan, phase string) error {
	terminal := filepath.Join(w.Config.StateDirectory, "authority", workload.Digest(workload.JSON(p)), "terminal.json")
	if _, e := regular(terminal, true); e == nil {
		return errors.New("prior attempt stopped; publication is forbidden")
	} else if !os.IsNotExist(e) {
		return e
	}
	if _, e := regular(w.publicationPath(p, phase), true); e == nil {
		return nil // Receipt will be validated; no remote mutation is repeated.
	} else if !os.IsNotExist(e) {
		return e
	}
	if _, e := regular(w.publicationPath(p, phase)+".intent", true); e == nil {
		return errors.New("publication intent without receipt; outcome requires inspection")
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}

func (w *Workflow) precedingPublication(p Plan, phase string) (*Publication, error) {
	if !slices.Equal(p.Phases, publicationPhases(p.Parent == p.BaseCommit)) {
		return nil, errors.New("invalid fixed publication sequence")
	}
	index := slices.Index(p.Phases, phase)
	if index < 0 {
		return nil, errors.New("phase not in approved plan")
	}
	parent := p.Parent
	var prior *Publication
	for _, step := range p.Phases[:index] {
		receipt, e := w.readPublication(p, step, parent, p.PhaseSHA256[step])
		if e != nil {
			return nil, e
		}
		parent, prior = receipt.Commit, &receipt
	}
	return prior, nil
}

func sameFiles(a, b Files) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
func (w *Workflow) commit(ctx context.Context, files Files, parent, message string) (string, error) {
	index, e := os.CreateTemp(w.Config.StateDirectory, "git-index-")
	if e != nil {
		return "", e
	}
	index.Close()
	os.Remove(index.Name())
	defer os.Remove(index.Name())
	run := func(input []byte, args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, "git", publicationGitArgs(args...)...)
		c.Dir = w.repo()
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_INDEX_FILE=" + index.Name(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Atlas S2", "GIT_AUTHOR_EMAIL=atlas-s2@localhost", "GIT_COMMITTER_NAME=Atlas S2", "GIT_COMMITTER_EMAIL=atlas-s2@localhost"}
		c.Stdin = bytes.NewReader(input)
		b, e := c.Output()
		if e != nil {
			return nil, errors.New("local Git tree construction failed")
		}
		return b, nil
	}
	if _, e = run(nil, "read-tree", parent); e != nil {
		return "", e
	}
	parentTree, e := w.ReadTree(ctx, parent)
	if e != nil {
		return "", e
	}
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if bytes.Equal(files[name], parentTree[name]) {
			continue
		}
		if strings.ContainsAny(name, "\n\r\x00\t") || filepath.Clean(name) != name || filepath.IsAbs(name) || strings.HasPrefix(name, "../") {
			return "", errors.New("unsafe output path")
		}
		blob, e := run(files[name], "hash-object", "-w", "--stdin")
		if e != nil {
			return "", e
		}
		if _, e = run(nil, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(string(blob)), name); e != nil {
			return "", e
		}
	}
	tree, e := run(nil, "write-tree")
	if e != nil {
		return "", e
	}
	b, e := run([]byte(message+"\n"), "commit-tree", strings.TrimSpace(string(tree)), "-p", parent)
	if e != nil {
		return "", e
	}
	revision := strings.TrimSpace(string(b))
	if !fullSHA.MatchString(revision) {
		return "", errors.New("invalid generated commit")
	}
	return revision, nil
}
