package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const ScopeSHA256 = "f176b3542f8b2b0c29a4ee6bef6bcd383c776f515e2e1be3482025c838105e35"
const StagesSHA256 = "ba9594ca79e51a6bbc5456a7a1a8a754fb6ef39b331580f7fc9a8cd020d7e15c"

type ScopeObject struct {
	Identity      string `json:"identity"`
	OldOwner      string `json:"oldOwner"`
	NewOwner      string `json:"newOwner"`
	DesiredSHA256 string `json:"desiredSHA256"`
}
type Scope struct {
	OldLayoutCommit         string            `json:"oldLayoutCommit"`
	NewLayoutCommit         string            `json:"newLayoutCommit"`
	SourceFilesSHA256       map[string]string `json:"sourceFilesSHA256"`
	ControllerPayloadSHA256 string            `json:"controllerPayloadSHA256"`
	Objects                 []ScopeObject     `json:"objects"`
}
type Stage struct {
	Name         string            `json:"name"`
	Previous     *string           `json:"previous"`
	Outcome      string            `json:"outcome"`
	ActiveOwner  *string           `json:"activeOwner"`
	Owners       map[string]string `json:"owners"`
	Applications map[string]string `json:"applications"`
	AtlasGate    bool              `json:"atlasGate"`
}
type Revision struct {
	Commit      string            `json:"commit"`
	FilesSHA256 map[string]string `json:"filesSHA256"`
}
type Phase struct {
	Stage        Stage                             `json:"stage"`
	Revision     string                            `json:"revision"`
	Applications []observation.ExpectedApplication `json:"applications"`
}
type Plan struct {
	PublicationRefresh bool                       `json:"publicationRefresh,omitempty"`
	EvidenceModel      string                     `json:"evidenceModel,omitempty"`
	Continuation       *ContinuationBinding       `json:"continuation,omitempty"`
	AuthorityInputs    map[string]string          `json:"authorityInputs"`
	LockedTools        map[string]string          `json:"lockedTools"`
	Schema             int                        `json:"schema"`
	Ceremony           string                     `json:"ceremony"`
	Target             observation.Target         `json:"target"`
	Implementation     observation.Implementation `json:"implementation"`
	Repository         string                     `json:"repository"`
	Branch             string                     `json:"branch"`
	ScopeSHA256        string                     `json:"scopeSHA256"`
	StagesSHA256       string                     `json:"stagesSHA256"`
	MaxStageSeconds    int                        `json:"maxStageSeconds"`
	BaselineRevision   string                     `json:"baselineRevision"`
	Revisions          map[string]Revision        `json:"revisions"`
	Scope              Scope                      `json:"scope"`
	Phases             []Phase                    `json:"phases"`
}

func LoadContracts(root string) (Scope, []Stage, error) {
	var scope Scope
	var stages []Stage
	for _, check := range []struct {
		path, sha string
		dst       any
	}{{"inventory.json", ScopeSHA256, &scope}, {"stages.json", StagesSHA256, &stages}} {
		b, e := os.ReadFile(filepath.Join(root, "experiments/foundation-ownership", check.path))
		if e != nil {
			return scope, nil, e
		}
		if observation.SHA(b) != check.sha {
			return scope, nil, errors.New("OT-1 contract differs from compiled scope/stages")
		}
		if e = observation.Decode(b, check.dst, true); e != nil {
			return scope, nil, e
		}
	}
	if len(scope.Objects) != 13 || len(stages) != 29 || scope.OldLayoutCommit != OldLayoutCommit || scope.NewLayoutCommit != NewLayoutCommit {
		return scope, nil, errors.New("invalid immutable OT-1 contract")
	}
	return scope, stages, nil
}
func ParseIdentity(identity string) (observation.Ref, error) {
	// API versions contain a slash only for non-core groups. Kind starts uppercase.
	parts := strings.Split(identity, "/")
	var ref observation.Ref
	switch len(parts) {
	case 4:
		ref = observation.Ref{APIVersion: parts[0], Kind: parts[1], Namespace: parts[2], Name: parts[3]}
	case 5:
		ref = observation.Ref{APIVersion: parts[0] + "/" + parts[1], Kind: parts[2], Namespace: parts[3], Name: parts[4]}
	default:
		return ref, errors.New("invalid scope identity")
	}
	return ref, ref.Validate()
}
func domain(owner string) string { return strings.TrimSuffix(owner, "-foundation") }
func destination(owner string) string {
	switch owner {
	case Source:
		return "argocd"
	case "secrets-foundation":
		return "atlas-secrets"
	case "observability-foundation":
		return "atlas-monitoring"
	case "storage-foundation":
		return "atlas-storage"
	}
	return ""
}
func Options(strict bool) []any {
	mode := "false"
	if strict {
		mode = "true"
	}
	return []any{"ServerSideApply=true", "FailOnSharedResource=" + mode}
}
func Application(owner string, strict bool) (observation.Object, error) {
	ns := destination(owner)
	if ns == "" {
		return nil, errors.New("Application is outside OT-1 owner set")
	}
	leaf := domain(owner)
	if owner == Source {
		leaf = "capabilities"
	}
	path := "gitops/platform/foundation/" + leaf + "/overlays/development"
	return observation.Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": observation.Object{"name": owner, "namespace": "argocd", "annotations": observation.Object{"argocd.argoproj.io/sync-wave": "-100", "argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}}, "spec": observation.Object{"project": "platform-project", "source": observation.Object{"repoURL": developmentprofile.Repository, "targetRevision": developmentprofile.OT1Revision, "path": path}, "destination": observation.Object{"server": "https://kubernetes.default.svc", "namespace": ns}, "syncPolicy": observation.Object{"syncOptions": Options(strict)}}}, nil
}
func ReadGraph(ctx context.Context, repo, sha string) ([]observation.ExpectedApplication, error) {
	var out []observation.ExpectedApplication
	seen := map[string]bool{}
	for _, path := range []string{"platform/development/bootstrap/root.json", "gitops/root/overlays/development/resources.json", ApplicationsPath, "gitops/workloads/applications/overlays/development/resources.json"} {
		b, e := sourceFile(ctx, repo, sha, path)
		if e != nil {
			return nil, e
		}
		objects, e := platform.DecodeJSONManifests(b)
		if e != nil {
			return nil, e
		}
		for _, o := range objects {
			ref := observation.Reference(o)
			if ref.Kind != "Application" || ref.APIVersion != "argoproj.io/v1alpha1" || ref.Namespace != "argocd" || seen[ref.Name] {
				return nil, errors.New("invalid or duplicate control graph")
			}
			seen[ref.Name] = true
			out = append(out, observation.ExpectedApplication{Name: ref.Name, Spec: observation.Map(o["spec"]), Revision: sha})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func renderHashes(ctx context.Context, repo, sha string) (map[string]string, error) {
	b, e := git(ctx, repo, "ls-tree", "-r", "--name-only", sha, "--", "gitops", "platform/development")
	if e != nil {
		return nil, e
	}
	hashes := map[string]string{}
	for _, path := range strings.Fields(string(b)) {
		data, e := sourceFile(ctx, repo, sha, path)
		if e != nil {
			return nil, e
		}
		hashes[path] = observation.SHA(data)
	}
	return hashes, nil
}
func commitLocal(ctx context.Context, repo, message string) (string, error) {
	if _, e := git(ctx, repo, "add", "--all"); e != nil {
		return "", e
	}
	if _, e := git(ctx, repo, "-c", "user.name=Atlas OT-1 preparation", "-c", "user.email=atlas-ot1@invalid.local", "commit", "-m", message); e != nil {
		return "", e
	}
	b, e := git(ctx, repo, "rev-parse", "HEAD")
	return strings.TrimSpace(string(b)), e
}
func dropOwners(repo string) error {
	o, e := readObject(repo, ApplicationsPath)
	if e != nil {
		return e
	}
	items := []any{}
	for _, raw := range observation.Slice(o["items"]) {
		item := observation.Map(raw)
		if destination(observation.String(observation.At(item, "metadata", "name"))) == "" {
			items = append(items, item)
		}
	}
	o["items"] = items
	return jsonFile(repo, ApplicationsPath, o)
}

// CompilePlan uses a separate private clone. Inputs must already contain the
// full, pre-split desired platform and the caller's explicit target binding.
// It does not create that cluster, read credentials, publish Git or execute plan.
func CompilePlan(ctx context.Context, implementationRoot, baselineRepo, outputRepo string, target observation.Target, impl observation.Implementation, tools platform.Tools) (Plan, error) {
	var plan Plan
	var e error
	implementationRoot, e = filepath.Abs(implementationRoot)
	if e != nil {
		return plan, e
	}
	baselineRepo, e = filepath.Abs(baselineRepo)
	if e != nil {
		return plan, e
	}
	outputRepo, e = filepath.Abs(outputRepo)
	if e != nil {
		return plan, e
	}
	if e := target.Validate(); e != nil {
		return plan, e
	}
	if target.Cluster != developmentprofile.OT1Cluster || impl.Dirty || !observation.FullSHA(impl.Revision) || !observation.Hash(impl.BinarySHA256) {
		return plan, errors.New("exact OT-1 target and clean implementation binary binding required")
	}
	scope, stages, e := LoadContracts(implementationRoot)
	if e != nil {
		return plan, e
	}
	p, e := platform.Load(baselineRepo, tools)
	if e != nil {
		return plan, e
	}
	if p.Config.Revision != developmentprofile.OT1Revision {
		return plan, errors.New("baseline is not isolated OT-1")
	}
	if _, ok := p.Capabilities.Catalog.Components[Source]; !ok {
		return plan, errors.New("baseline must use the pre-split catalog")
	}
	if !reflect.DeepEqual(p.Capabilities.Enabled.Capabilities, []string{"monitoring", "object-storage", "storage-monitoring"}) {
		return plan, errors.New("complete capability baseline required before planning transfer")
	}
	if e = p.ValidateCapabilityActivation(); e != nil {
		return plan, e
	}
	if _, e = p.Check(ctx); e != nil {
		return plan, e
	}
	dirty, e := git(ctx, baselineRepo, "status", "--porcelain", "--untracked-files=all")
	if e != nil || len(dirty) > 0 {
		return plan, errors.New("baseline checkout must be clean and committed")
	}
	baseline, e := git(ctx, baselineRepo, "rev-parse", "HEAD")
	if e != nil {
		return plan, e
	}
	initial := strings.TrimSpace(string(baseline))
	if _, e = os.Lstat(outputRepo); !os.IsNotExist(e) {
		return plan, errors.New("plan clone already exists; preserve previous preparation")
	}
	if e = observation.PrivateDirectory(filepath.Dir(outputRepo)); e != nil {
		return plan, e
	}
	if _, e = git(ctx, baselineRepo, "clone", "--no-hardlinks", baselineRepo, outputRepo); e != nil {
		return plan, e
	}
	if e = os.Chmod(outputRepo, 0700); e != nil {
		return plan, e
	}
	if _, e = git(ctx, outputRepo, "remote", "set-url", "origin", developmentprofile.Repository); e != nil {
		return plan, e
	}
	oldCatalog, e := os.ReadFile(filepath.Join(outputRepo, CatalogPath))
	if e != nil {
		return plan, e
	}
	oldApps, e := os.ReadFile(filepath.Join(outputRepo, ApplicationsPath))
	if e != nil {
		return plan, e
	}
	projects, e := os.ReadFile(filepath.Join(outputRepo, ProjectsPath))
	if e != nil {
		return plan, e
	}
	// Manual strict manifests are part of the already-reviewed baseline Git.
	for _, owner := range append([]string{Source}, Targets...) {
		want, _ := Application(owner, true)
		b, e := os.ReadFile(filepath.Join(outputRepo, "experiments/foundation-ownership/applications", owner+".json"))
		if e != nil || observation.SHA(b) != observation.Digest(want) {
			return plan, errors.New("manual Application definition missing or changed in baseline Git")
		}
	}
	plan = Plan{PublicationRefresh: true, EvidenceModel: EvidenceModel, Schema: 1, Ceremony: "ot1-foundation-split/v1", Target: target, Implementation: impl, Repository: developmentprofile.Repository, Branch: developmentprofile.OT1Revision, ScopeSHA256: ScopeSHA256, StagesSHA256: StagesSHA256, MaxStageSeconds: 300, BaselineRevision: initial, Revisions: map[string]Revision{}, Scope: scope}
	plan.AuthorityInputs, plan.LockedTools, e = authorityInputs(ctx, outputRepo, initial)
	if e != nil {
		return plan, e
	}
	record := func(name, sha string) error {
		hashes, e := renderHashes(ctx, outputRepo, sha)
		if e == nil {
			plan.Revisions[name] = Revision{sha, hashes}
		}
		return e
	}
	if e = record("baseline", initial); e != nil {
		return plan, e
	}
	for _, name := range []string{"detached-mixed", "mixed-restored", "detached-forward", "forward-restored", "detached-reverse", "reverse-restored"} {
		switch name {
		case "detached-mixed", "detached-forward", "detached-reverse":
			e = dropOwners(outputRepo)
		case "mixed-restored", "reverse-restored":
			e = write(outputRepo, CatalogPath, oldCatalog)
			if e == nil {
				e = write(outputRepo, ApplicationsPath, oldApps)
			}
		case "forward-restored":
			var b []byte
			b, e = sourceFile(ctx, implementationRoot, NewLayoutCommit, CatalogPath)
			if e == nil {
				e = write(outputRepo, CatalogPath, b)
			}
			if e == nil {
				var next *platform.Project
				next, e = platform.Load(outputRepo, tools)
				if e == nil {
					var activation map[string][]byte
					activation, e = next.CapabilityActivation(next.Capabilities.Active)
					if e == nil {
						if !bytesEqual(activation[ProjectsPath], projects) {
							e = errors.New("AppProject content/permissions change across ownership split")
						} else {
							e = next.Write(activation)
						}
					}
				}
			}
		}
		if e != nil {
			return plan, e
		}
		sha, e := commitLocal(ctx, outputRepo, "OT-1 desired stage: "+name)
		if e != nil {
			return plan, e
		}
		if e = record(name, sha); e != nil {
			return plan, e
		}
	}
	for _, stage := range stages {
		name := revisionName(stage.Name)
		revision := plan.Revisions[name].Commit
		apps, e := ReadGraph(ctx, outputRepo, revision)
		if e != nil {
			return plan, e
		}
		if !stage.AtlasGate {
			for owner, mode := range stage.Applications {
				a, _ := Application(owner, mode == "strict")
				apps = append(apps, observation.ExpectedApplication{Name: owner, Spec: observation.Map(a["spec"]), Revision: revision})
			}
			sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
		}
		plan.Phases = append(plan.Phases, Phase{stage, revision, apps})
	}
	if e = ValidatePlan(plan, stages, scope); e != nil {
		return plan, e
	}
	return plan, VerifyRepositoryPlan(ctx, outputRepo, plan)
}
func bytesEqual(a, b []byte) bool { return string(a) == string(b) }
func revisionName(stage string) string {
	switch stage {
	case "BASELINE_ADOPTED":
		return "baseline"
	case "MIXED_ROLLBACK_VERIFIED":
		return "mixed-restored"
	case "FORWARD_VERIFIED":
		return "forward-restored"
	case "REVERSE_VERIFIED":
		return "reverse-restored"
	}
	if strings.HasPrefix(stage, "REVERSE_") {
		return "detached-reverse"
	}
	if strings.HasPrefix(stage, "FORWARD_") || stage == "SECOND_SOURCE_RELEASED" {
		return "detached-forward"
	}
	return "detached-mixed"
}
func ValidatePlan(p Plan, stages []Stage, scope Scope) error {
	if p.PublicationRefresh && (p.Continuation != nil || p.EvidenceModel != EvidenceModel) {
		return errors.New("publication refresh requires a new normal semantic plan")
	}
	if p.EvidenceModel != "" && p.EvidenceModel != EvidenceModel {
		return errors.New("unknown evidence model")
	}
	if p.Continuation != nil {
		if e := p.Continuation.Validate(); e != nil {
			return e
		}
		if p.EvidenceModel != EvidenceModel {
			return errors.New("continuation requires semantic evidence")
		}
	}
	if p.Schema != 1 || p.Ceremony != "ot1-foundation-split/v1" || p.Target.Cluster != developmentprofile.OT1Cluster || p.Target.Context != "kind-"+developmentprofile.OT1Cluster || p.Repository != developmentprofile.Repository || p.Branch != developmentprofile.OT1Revision || p.ScopeSHA256 != ScopeSHA256 || p.StagesSHA256 != StagesSHA256 || p.MaxStageSeconds != 300 || p.Implementation.Dirty || !observation.FullSHA(p.Implementation.Revision) || !observation.Hash(p.Implementation.BinarySHA256) {
		return errors.New("plan expands the approved OT-1 boundary")
	}
	if e := p.Target.Validate(); e != nil {
		return e
	}
	if !reflect.DeepEqual(p.Scope, scope) || len(p.Phases) != len(stages) || len(p.Revisions) != 7 || p.BaselineRevision != p.Revisions["baseline"].Commit {
		return errors.New("plan scope/stages/revisions differ")
	}
	for i, s := range stages {
		phase := p.Phases[i]
		if !reflect.DeepEqual(phase.Stage, s) || phase.Revision != p.Revisions[revisionName(s.Name)].Commit || !observation.FullSHA(phase.Revision) {
			return errors.New("plan skips or changes a phase")
		}
		seen := map[string]bool{}
		for _, a := range phase.Applications {
			if seen[a.Name] || a.Revision != phase.Revision || a.UID != "" {
				return errors.New("duplicate/misbound Application expectation")
			}
			seen[a.Name] = true
			if destination(a.Name) != "" && !s.AtlasGate {
				want, _ := Application(a.Name, s.Applications[a.Name] == "strict")
				if s.Applications[a.Name] == "" || observation.Digest(a.Spec) != observation.Digest(want["spec"]) {
					return errors.New("unreviewed ceremony Application spec")
				}
			}
		}
	}
	return nil
}
func VerifyRepositoryPlan(ctx context.Context, repo string, p Plan) error {
	if p.EvidenceModel == EvidenceModel {
		if e := validateSourceClosures(ctx, repo, p); e != nil {
			return e
		}
	}
	// Only these two local generated inputs may vary after BASELINE_ADOPTED.
	// The full project, credentials, frozen core and every resource stay byte-equal.
	hashes, versions, e := authorityInputs(ctx, repo, p.BaselineRevision)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(hashes, p.AuthorityInputs) || !reflect.DeepEqual(versions, p.LockedTools) {
		return errors.New("plan supply-chain bindings differ from baseline Git")
	}
	if e := VerifyScopeGit(ctx, repo, p.BaselineRevision, p.Scope); e != nil {
		return e
	}
	allowed := map[string]bool{CatalogPath: true, ApplicationsPath: true}
	for name, revision := range p.Revisions {
		changed, err := git(ctx, repo, "diff", "--name-only", p.BaselineRevision, revision.Commit, "--")
		if err != nil {
			return err
		}
		for _, path := range strings.Fields(string(changed)) {
			if !allowed[path] {
				return fmt.Errorf("unreviewed file change at %s: %s", name, path)
			}
		}
		actual, e := renderHashes(ctx, repo, revision.Commit)
		if e != nil || !reflect.DeepEqual(actual, revision.FilesSHA256) {
			return errors.New("planned Git/render content unavailable or changed")
		}
		for path, hash := range p.Revisions["baseline"].FilesSHA256 {
			if !allowed[path] && actual[path] != hash {
				return fmt.Errorf("unreviewed desired change at %s: %s", name, path)
			}
		}
		for path := range actual {
			if _, ok := p.Revisions["baseline"].FilesSHA256[path]; !ok && !allowed[path] {
				return errors.New("new unreviewed Git resource input")
			}
		}
	}
	baseAppBytes, e := sourceFile(ctx, repo, p.BaselineRevision, ApplicationsPath)
	if e != nil {
		return e
	}
	baseAppObjects, e := platform.DecodeJSONManifests(baseAppBytes)
	if e != nil {
		return e
	}
	oldCatalog, e := sourceFile(ctx, repo, p.BaselineRevision, CatalogPath)
	if e != nil {
		return e
	}
	newCatalog, e := sourceFile(ctx, repo, NewLayoutCommit, CatalogPath)
	if e != nil {
		return e
	}
	for _, phase := range p.Phases {
		appBytes, e := sourceFile(ctx, repo, phase.Revision, ApplicationsPath)
		if e != nil {
			return e
		}
		phaseObjects, e := platform.DecodeJSONManifests(appBytes)
		if e != nil {
			return e
		}
		if e = checkApplicationProjection(baseAppObjects, phaseObjects, phase.Stage); e != nil {
			return e
		}
		wantCatalog := oldCatalog
		name := revisionName(phase.Stage.Name)
		if name == "forward-restored" || name == "detached-reverse" {
			wantCatalog = newCatalog
		}
		catalog, e := sourceFile(ctx, repo, phase.Revision, CatalogPath)
		if e != nil || !bytesEqual(catalog, wantCatalog) {
			return errors.New("catalog escapes reviewed old/new layouts")
		}
		apps, e := ReadGraph(ctx, repo, phase.Revision)
		if e != nil {
			return e
		}
		if !phase.Stage.AtlasGate {
			for owner, mode := range phase.Stage.Applications {
				a, _ := Application(owner, mode == "strict")
				apps = append(apps, observation.ExpectedApplication{Name: owner, Spec: observation.Map(a["spec"]), Revision: phase.Revision})
			}
			sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
		}
		if observation.Digest(apps) != observation.Digest(phase.Applications) {
			return errors.New("Application plan differs from immutable Git graph")
		}
	}
	return nil
}

func VerifyScopeGit(ctx context.Context, repo, sha string, scope Scope) error {
	expected := map[string]ScopeObject{}
	for _, object := range scope.Objects {
		expected[object.Identity] = object
	}
	paths := map[string]string{Source: "gitops/platform/foundation/capabilities/overlays/development/resources.json", "secrets-foundation": "gitops/platform/foundation/secrets/overlays/development/resources.json", "observability-foundation": "gitops/platform/foundation/observability/overlays/development/resources.json", "storage-foundation": "gitops/platform/foundation/storage/overlays/development/resources.json"}
	counts := map[string]int{Source: 13, "secrets-foundation": 3, "observability-foundation": 4, "storage-foundation": 6}
	for owner, path := range paths {
		b, e := sourceFile(ctx, repo, sha, path)
		if e != nil {
			return e
		}
		objects, e := platform.DecodeJSONManifests(b)
		if e != nil {
			return e
		}
		if len(objects) != counts[owner] {
			return errors.New("foundation Git inventory differs from OT-1 scope")
		}
		seen := map[string]bool{}
		for _, o := range objects {
			id := observation.Reference(o).Key()
			want, ok := expected[id]
			if !ok || seen[id] || observation.Digest(o) != want.DesiredSHA256 || owner != Source && want.NewOwner != owner {
				return fmt.Errorf("foundation Git content/owner changed: %s", id)
			}
			seen[id] = true
		}
	}
	payload, e := sourceFile(ctx, repo, sha, "gitops/platform/management/sealed-secrets/overlays/development/rendered.yaml")
	if e != nil {
		return e
	}
	if observation.SHA(payload) != scope.ControllerPayloadSHA256 {
		return errors.New("secrets-controller payload differs from the reviewed unchanged owner")
	}
	return nil
}

func authorityInputs(ctx context.Context, repo, sha string) (map[string]string, map[string]string, error) {
	hashes := map[string]string{}
	versions := map[string]string{}
	for _, path := range []string{"go.mod", "versions.lock.json", "platform/development/versions.lock.json", "platform/development/capabilities/versions.lock.json", "platform/development/capabilities/kubeseal.lock.json", "platform/development/kubernetes-api-scope.json"} {
		b, e := sourceFile(ctx, repo, sha, path)
		if e != nil {
			return nil, nil, e
		}
		hashes[path] = observation.SHA(b)
		if path == "versions.lock.json" {
			var o observation.Object
			if e = observation.Decode(b, &o, false); e != nil {
				return nil, nil, e
			}
			for _, key := range []string{"go", "helm", "kind", "kubectl", "kubernetes"} {
				versions[key] = observation.String(o[key])
				if versions[key] == "" {
					return nil, nil, errors.New("missing locked tool version")
				}
			}
		}
	}
	return hashes, versions, nil
}

// File allowlists alone are insufficient: a caller could alter an unrelated
// child inside the allowed Applications file. Enforce the exact object delta.
func checkApplicationProjection(baseline, actual []observation.Object, stage Stage) error {
	want := map[string]observation.Object{}
	var source observation.Object
	for _, app := range baseline {
		name := observation.Reference(app).Name
		if destination(name) == "" {
			want[name] = app
		} else if name == Source {
			source = app
		} else {
			return errors.New("baseline already contains split owners")
		}
	}
	if source == nil {
		return errors.New("old foundation Application missing from baseline")
	}
	if stage.AtlasGate {
		for owner := range stage.Applications {
			template, e := Application(owner, true)
			if e != nil {
				return e
			}
			app := observation.Clone(source)
			observation.Map(app["metadata"])["name"] = owner
			observation.Map(observation.At(app, "spec", "source"))["path"] = observation.At(template, "spec", "source", "path")
			observation.Map(observation.At(app, "spec", "destination"))["namespace"] = observation.At(template, "spec", "destination", "namespace")
			want[owner] = app
		}
	}
	if len(actual) != len(want) {
		return errors.New("parent projection adds or drops an unrelated Application")
	}
	for _, app := range actual {
		name := observation.Reference(app).Name
		if expected, ok := want[name]; !ok || observation.Digest(app) != observation.Digest(expected) {
			return errors.New("parent projection changes an unapproved Application field")
		}
		delete(want, name)
	}
	return nil
}
