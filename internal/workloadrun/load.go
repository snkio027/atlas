// Package workloadrun connects the pure S2 compiler to a bounded D1 extension.
// Compilation is offline. Publication and functional probes require an exact
// plan approval; observation never repairs, refreshes or syncs Applications.
package workloadrun

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/installation"
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/workload"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
)

type Config struct {
	Purpose             string `json:"purpose,omitempty"`
	Schema              int    `json:"schema"`
	InstallationConfig  string `json:"installationConfig"`
	InstallationPackage string `json:"installationPackage"`
	IntentDirectory     string `json:"intentDirectory"`
	ProductSource       string `json:"productSource"`
	StateDirectory      string `json:"stateDirectory"`
	ImageArchive        string `json:"imageArchive"`
	ImageSHA256         string `json:"imageSHA256"`
}
type Workflow struct {
	observationPlan *Plan
	UnpublishedPlan string
	Config          Config
	Install         installation.Workflow
	Model           *workload.Model
	Context         workload.CompileContext
	BinarySHA256    string
	BuildSource     string
	BuildGoVersion  string
	BuildDirty      bool
	Progress        func(string)
	toolsVerified   bool
	trees           map[string]Files
}
type Object = map[string]any
type Files = map[string][]byte

var fullSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)

func at(o Object, keys ...string) any {
	var v any = o
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}
func str(v any) string     { s, _ := v.(string); return s }
func array(v any) []any    { a, _ := v.([]any); return a }
func mapping(v any) Object { o, _ := v.(map[string]any); return o }
func command(ctx context.Context, dir string, input []byte, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Stdin = bytes.NewReader(input)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	b, e := c.Output()
	if e != nil {
		return b, fmt.Errorf("%s failed (output withheld): %w", filepath.Base(name), e)
	}
	return b, nil
}
func regular(path string, private bool) ([]byte, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	real, e := filepath.EvalSymlinks(absolute)
	if e != nil {
		return nil, e
	}
	if real != absolute {
		return nil, errors.New("symlink input is forbidden")
	}
	st, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<20 || private && st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe input file")
	}
	return os.ReadFile(path)
}
func save(path string, b []byte, immutable bool) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	real, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil || real != filepath.Dir(path) {
		return errors.New("unsafe output directory")
	}
	st, e := os.Stat(real)
	if e != nil || st.Mode().Perm()&0077 != 0 {
		return errors.New("output directory must be owner-only")
	}
	if old, e := regular(path, true); e == nil {
		if bytes.Equal(old, b) {
			return nil
		}
		if immutable {
			return errors.New("existing immutable evidence differs")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".staging-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if immutable {
		if e = os.Link(f.Name(), path); e != nil {
			return e
		}
		return nil
	}
	return os.Rename(f.Name(), path)
}
func Load(configPath string) (*Workflow, error) {
	b, e := regularAuthored(configPath)
	if e != nil {
		return nil, e
	}
	w := &Workflow{}
	if e = workload.StrictDecode(b, &w.Config); e != nil {
		return nil, e
	}
	if w.Config.Schema != 1 || (w.Config.Purpose != "" && w.Config.Purpose != "application") {
		return nil, errors.New("unknown S2 config schema")
	}
	for _, p := range []string{w.Config.InstallationConfig, w.Config.InstallationPackage, w.Config.IntentDirectory, w.Config.ProductSource, w.Config.StateDirectory} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return nil, errors.New("S2 paths must be absolute clean paths")
		}
	}
	b, e = regularAuthored(w.Config.InstallationConfig)
	if e != nil {
		return nil, e
	}
	if e = installation.Decode(b, &w.Install.Config); e != nil {
		return nil, e
	}
	if strings.HasPrefix(w.Config.StateDirectory+"/", w.Install.Config.StateDirectory+"/") || strings.HasPrefix(w.Config.StateDirectory+"/", w.Config.ProductSource+"/gitops/") {
		return nil, errors.New("S2 state must be separate from D1 state and generated Git")
	}
	b, e = regular(filepath.Join(w.Config.InstallationPackage, "runtime.json"), false)
	if e != nil {
		return nil, e
	}
	w.Install.ProductDigest = installation.Digest(b)
	if e = installation.Decode(b, &w.Install.Product); e != nil {
		return nil, e
	}
	b, e = regular(filepath.Join(w.Config.InstallationPackage, "atlas-install"), false)
	if e != nil {
		return nil, e
	}
	w.Install.BinaryDigest = installation.Digest(b)
	if e = w.Install.Open(false); e != nil {
		return nil, e
	}
	if !w.Install.Record.Complete {
		return nil, errors.New("S2 requires a completed D1 installation")
	}
	executable, e := os.Executable()
	if e != nil {
		return nil, e
	}
	b, e = os.ReadFile(executable)
	if e != nil {
		return nil, e
	}
	w.BinarySHA256 = workload.Digest(b)
	if info, ok := debug.ReadBuildInfo(); ok {
		w.BuildGoVersion = info.GoVersion
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				w.BuildSource = s.Value
			case "vcs.modified":
				w.BuildDirty = s.Value == "true"
			}
		}
	}
	in, e := workload.ReadIntent(w.Config.IntentDirectory)
	if e != nil {
		return nil, e
	}
	w.Model, e = workload.Resolve(in, true)
	if e != nil {
		return nil, e
	}
	p, e := platform.Load(w.Config.ProductSource, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if e != nil {
		return nil, e
	}
	if e = p.VerifyArtifacts(); e != nil {
		return nil, e
	}
	model, e := p.ObservationResourceModel()
	if e != nil {
		return nil, e
	}
	w.Context = workload.CompileContext{Repository: w.Install.Config.Repository, Branch: w.Install.Config.Branch, ProductSHA256: w.Install.ProductDigest, CompilerSHA256: w.BinarySHA256, InstallID: w.Install.Record.InstallID, CertificateSHA256: w.Install.Record.CertificateSHA256, HTTPSPort: w.Install.Config.HTTPSPort, ResourceModel: model}
	return w, nil
}
func (w *Workflow) Lock() (func(), error) {
	// The same D1 lock excludes installer/S2 concurrent writes, without modifying
	// the original installation record, trust receipt or authority bundle.
	return w.Install.Lock()
}
func (w *Workflow) repo() string { return filepath.Join(w.Config.StateDirectory, "deployment") }
func (w *Workflow) git(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	return command(ctx, w.repo(), input, "git", publicationGitArgs(args...)...)
}

// Match the established D1 transport policy without changing the D1 engine.
// Authentication stays in the existing gh store; no token enters argv, state,
// generated Git or logs. Per-process options never change the user's Git config.
func publicationGitArgs(args ...string) []string {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", "-c", "user.name=Atlas S2", "-c", "user.email=atlas-s2@localhost", "-c", "commit.gpgsign=false"}
	return append(prefix, args...)
}
func (w *Workflow) checkPublicationAccess(ctx context.Context) error {
	name := strings.TrimSuffix(strings.TrimPrefix(w.Install.Config.Repository, "https://github.com/"), ".git")
	raw, e := command(ctx, w.Config.StateDirectory, nil, "gh", "api", "--hostname", "github.com", "repos/"+name, "--jq", "{full_name,private,archived,push:.permissions.push}")
	if e != nil {
		return fmt.Errorf("GitHub publication access unavailable: %w", e)
	}
	return validatePublicationAccess(name, raw)
}
func validatePublicationAccess(name string, raw []byte) error {
	var access struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
		Archived bool   `json:"archived"`
		Push     bool   `json:"push"`
	}
	if e := workload.StrictDecode(raw, &access); e != nil {
		return errors.New("invalid GitHub publication permission evidence")
	}
	if !strings.EqualFold(access.FullName, name) || access.Private || access.Archived || !access.Push {
		return errors.New("deployment repository must be the exact public, active, writable GitHub repository")
	}
	return nil
}
func (w *Workflow) PrepareRepository(ctx context.Context) error {
	if e := os.MkdirAll(w.repo(), 0700); e != nil {
		return e
	}
	if _, e := os.Stat(filepath.Join(w.repo(), "HEAD")); os.IsNotExist(e) {
		if _, e = w.git(ctx, nil, "init", "--bare"); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	_, e := w.git(ctx, nil, "-c", "protocol.file.allow=never", "fetch", "--no-tags", w.Install.Config.Repository, "refs/heads/"+w.Install.Config.Branch)
	return e
}
func (w *Workflow) ReadTree(ctx context.Context, revision string) (Files, error) {
	if f := w.trees[revision]; f != nil {
		return f, nil
	}
	if !fullSHA.MatchString(revision) {
		return nil, errors.New("full deployment commit required")
	}
	b, e := w.git(ctx, nil, "ls-tree", "-rz", revision)
	if e != nil {
		return nil, e
	}
	files := Files{}
	for _, entry := range bytes.Split(b, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		head, name, ok := strings.Cut(string(entry), "\t")
		parts := strings.Fields(head)
		if !ok || len(parts) != 3 || (parts[0] != "100644" && parts[0] != "100755" && parts[0] != "120000") || parts[1] != "blob" || name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.HasPrefix(name, "../") {
			return nil, errors.New("non-regular deployment tree entry")
		}
		if (strings.HasPrefix(name, "gitops/") || strings.HasPrefix(name, "platform/")) && parts[0] != "100644" {
			return nil, errors.New("authored/generated paths must be regular non-executable files")
		}
		data, e := w.git(ctx, nil, "cat-file", "blob", parts[2])
		if e != nil {
			return nil, e
		}
		files[name] = data
	}
	if w.trees == nil {
		w.trees = map[string]Files{}
	}
	w.trees[revision] = files
	return files, nil
}
func (w *Workflow) LoadBase(ctx context.Context) error {
	files, e := w.ReadTree(ctx, w.Install.Record.FullCommit)
	if e != nil {
		return e
	}
	expected, e := w.Install.Product.Project(w.Install.Config, true)
	if e != nil {
		return e
	}
	for name, want := range expected {
		if !strings.HasPrefix(name, "gitops/") || name == workload.CredentialsPath {
			continue
		}
		if !bytes.Equal(files[name], want) {
			return fmt.Errorf("D1 base differs from locked product: %s", name)
		}
	}
	if workload.Digest(files[workload.CredentialsPath]) != w.Install.Record.SealedSHA256 {
		return errors.New("D1 ciphertext baseline differs")
	}
	w.Context.Base = files
	return nil
}
func (w *Workflow) remote(ctx context.Context) (string, error) {
	b, e := w.git(ctx, nil, "ls-remote", "--exit-code", w.Install.Config.Repository, "refs/heads/"+w.Install.Config.Branch)
	if e != nil {
		return "", e
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 || !fullSHA.MatchString(fields[0]) || fields[1] != "refs/heads/"+w.Install.Config.Branch {
		return "", errors.New("ambiguous deployment ref")
	}
	return fields[0], nil
}
func (w *Workflow) Compile(phase string) (workload.Result, error) {
	var a *workload.Artifacts
	if phase == "consumer" && len(w.Model.Intent.Bindings) > 0 {
		b, e := regular(filepath.Join(w.Config.StateDirectory, "artifacts.json"), true)
		if e != nil {
			return workload.Result{}, e
		}
		a, e = workload.DecodeArtifacts(b)
		if e != nil {
			return workload.Result{}, e
		}
	}
	return workload.Compile(w.Context, w.Model, phase, a)
}
func (w *Workflow) WriteResult(r workload.Result) error {
	// Content-addressed output means failed compilation never replaces prior output.
	dir := filepath.Join(w.Config.StateDirectory, "compiled", platform.BundleDigest(r.Files))
	keys := make([]string, 0, len(r.Files))
	for k := range r.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if e := save(filepath.Join(dir, name), r.Files[name], true); e != nil {
			return e
		}
	}
	return save(filepath.Join(w.Config.StateDirectory, "latest-compile.json"), workload.JSON(Object{"directory": dir, "inventory": r.Inventory}), false)
}
func (w *Workflow) kubeCommand(args ...string) (string, []string, error) {
	if !w.toolsVerified {
		if e := installation.VerifyTools(w.Install.Config.StateDirectory, w.Install.Product.Tools); e != nil {
			return "", nil, e
		}
		w.toolsVerified = true
	}
	p := filepath.Join(w.Install.Config.StateDirectory, "runtime/.state/kubeconfig")
	b, e := regular(p, true)
	if e != nil {
		return "", nil, e
	}
	h, e := regular(p+".sha256", true)
	if e != nil || workload.Digest(b) != strings.TrimSpace(string(h)) {
		return "", nil, errors.New("kubeconfig binding differs")
	}
	return filepath.Join(installation.ToolDirectory(w.Install.Config.StateDirectory, w.Install.Product.Tools), "kubectl"), append([]string{"--kubeconfig", p, "--context", "kind-" + w.Install.Config.Cluster, "--request-timeout=30s"}, args...), nil
}
func (w *Workflow) kube(ctx context.Context, args ...string) ([]byte, error) {
	tool, argv, e := w.kubeCommand(args...)
	if e != nil {
		return nil, e
	}
	return command(ctx, w.Config.StateDirectory, nil, tool, argv...)
}
func (w *Workflow) get(ctx context.Context, kind, ns, name string) (Object, error) {
	// An absent name requests a collection. An explicit empty positional
	// argument is rejected by kubectl before any API read.
	args := []string{"get", kind}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "-o", "json", "--show-managed-fields=true")
	if ns != "" {
		args = append(args, "-n", ns)
	}
	b, e := w.kube(ctx, args...)
	if e != nil {
		return nil, e
	}
	var o Object
	e = json.Unmarshal(b, &o)
	return o, e
}
func (w *Workflow) bind(ctx context.Context) error {
	o, e := w.get(ctx, "namespace", "", "kube-system")
	if e != nil {
		return e
	}
	if at(o, "metadata", "uid") != w.Install.Record.ClusterUID {
		return errors.New("cluster UID differs")
	}
	return nil
}

func regularAuthored(path string) ([]byte, error) {
	p, err := authored.Resolve(path)
	if err != nil {
		return nil, err
	}
	b, err := regular(p, false)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(p) == ".yaml" {
		return authored.JSON(b)
	}
	return b, nil
}
