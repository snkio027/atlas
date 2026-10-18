// Package appworkspace manages local user-facing bindings. It delegates all
// deployment authority to workloadrun and never selects a kubectl context.
package appworkspace

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/oci"
	"atlas-refactor/internal/workload"
	"atlas-refactor/internal/workloadrun"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Schema              int    `json:"schema"`
	InstallationConfig  string `json:"installationConfig"`
	InstallationPackage string `json:"installationPackage"`
	ProductSource       string `json:"productSource"`
	Artifact            string `json:"artifact"`
}
type Artifact struct {
	Schema    int    `json:"schema"`
	Archive   string `json:"archive"`
	SHA256    string `json:"sha256"`
	Reference string `json:"reference"`
}

func Read(path string, private bool) ([]byte, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	if real != p {
		return nil, errors.New("symlink input is forbidden")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<20 || private && st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe or oversized local input")
	}
	return os.ReadFile(p)
}
func Write(path string, b []byte, exclusive bool) error { return writeFile(path, b, exclusive, true) }
func writeFile(path string, b []byte, exclusive, private bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return errors.New("unsafe output path")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || private && st.Mode().Perm()&0077 != 0 {
		return errors.New("workspace output directory must be owner-only")
	}
	if _, err := os.Lstat(path); err == nil {
		if exclusive {
			return errors.New("output already exists")
		}
		if _, err = Read(path, private); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".staging-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if !private {
		if err = f.Chmod(0644); err != nil {
			_ = f.Close()
			return err
		}
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if exclusive {
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func artifact(path string) (Artifact, error) {
	var a Artifact
	b, err := Read(path, false)
	if err != nil {
		return a, err
	}
	if err = workload.StrictDecode(b, &a); err != nil {
		return a, err
	}
	if a.Schema != 1 || filepath.Base(a.Archive) != a.Archive || a.Archive == "." {
		return a, errors.New("artifact archive must be a sibling filename")
	}
	archive := filepath.Join(filepath.Dir(path), a.Archive)
	raw, err := Read(archive, true)
	if err != nil {
		return a, err
	}
	if workload.Digest(raw) != a.SHA256 {
		return a, errors.New("artifact checksum differs")
	}
	if err = oci.VerifyApplication(archive, a.Reference); err != nil {
		return a, err
	}
	a.Archive = archive
	return a, nil
}
func Package(binary, tag, out string) (Artifact, error) {
	var a Artifact
	b, err := Read(binary, false)
	if err != nil {
		return a, err
	}
	raw, ref, err := oci.BuildStatic(b, tag)
	if err != nil {
		return a, err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return a, err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return a, err
	}
	a = Artifact{1, "image.oci.tar", workload.Digest(raw), ref}
	if err = Write(filepath.Join(out, a.Archive), raw, true); err != nil {
		return a, err
	}
	if err = oci.VerifyApplication(filepath.Join(out, a.Archive), ref); err != nil {
		return a, err
	}
	return a, Write(filepath.Join(out, "image.json"), workload.JSON(a), true)
}

type InitOptions struct {
	Root, Instance, ProductSource, Artifact, Project, Name, Owner string
	S3                                                            bool
}

func Init(o InitOptions) error {
	for _, p := range []*string{&o.Root, &o.Instance, &o.ProductSource, &o.Artifact} {
		a, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = a
	}
	a, err := artifact(o.Artifact)
	if err != nil {
		return err
	}
	installConfig, installPackage, err := instancePaths(o.Instance)
	if err != nil {
		return err
	}
	c := Config{1, installConfig, installPackage, o.ProductSource, o.Artifact}
	// Verify the instance's expected public layout before creating user files.
	for _, p := range []string{c.InstallationConfig, filepath.Join(c.InstallationPackage, "runtime.json"), filepath.Join(c.ProductSource, "versions.lock.json")} {
		if _, err = Read(p, false); err != nil {
			return fmt.Errorf("instance/product input %s: %w", p, err)
		}
	}
	in := workload.Intent{
		Project: workload.Project{Schema: 1, Kind: "Project", Name: o.Project, Owner: o.Owner,
			Quota: workload.Quota{Pods: 8, RequestsCPU: 1000, LimitsCPU: 4000, RequestsMemory: 1024, LimitsMemory: 2048}, CapabilityAccess: []workload.Grant{}},
		Workloads: []workload.Workload{{Schema: 1, Kind: "Workload", Type: "WebService", Project: o.Project, Name: o.Name, Image: a.Reference, Port: 8080, Replicas: 1,
			Resources: workload.Resources{Requests: workload.Quantity{CPU: 100, Memory: 128}, Limits: workload.Quantity{CPU: 500, Memory: 256}},
			Exposure:  workload.Exposure{Hostname: o.Name + ".atlas.test", TLS: true}, Observability: workload.Observability{Metrics: true}}},
		Bindings: []workload.Binding{},
	}
	if o.S3 {
		in.Project.CapabilityAccess = []workload.Grant{{Capability: "object-storage", Bucket: "uploads", Access: "read-write"}}
		in.Bindings = []workload.Binding{{Schema: 1, Kind: "CapabilityBinding", Project: o.Project, Name: "object-storage", Workload: o.Name, Capability: "object-storage", Bucket: "uploads", Access: "read-write"}}
	}
	m, err := workload.Resolve(in, true)
	if err != nil {
		return err
	}
	if err = os.Mkdir(o.Root, 0700); err != nil {
		return fmt.Errorf("choose a new workspace directory: %w", err)
	}
	human, err := m.HumanAuthored()
	if err != nil {
		return err
	}
	for n, b := range human {
		if err = writeFile(filepath.Join(o.Root, n), b, true, false); err != nil {
			return err
		}
	}
	if err = Write(filepath.Join(o.Root, ".gitignore"), []byte("/.atlas/\n/app.yaml\n"), true); err != nil {
		return err
	}
	if err = writeConfig(filepath.Join(o.Root, "app.yaml"), c, true); err != nil {
		return err
	}
	_, _, err = Load(o.Root)
	return err
}
func ConfigAt(root string) (Config, error) {
	var c Config
	b, err := readAuthored(filepath.Join(root, "app.yaml"), true)
	if err != nil {
		return c, err
	}
	if err = workload.StrictDecode(b, &c); err != nil {
		return c, err
	}
	if c.Schema != 1 {
		return c, errors.New("unsupported app workspace")
	}
	for _, p := range []string{c.InstallationConfig, c.InstallationPackage, c.ProductSource, c.Artifact} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return c, errors.New("workspace binding must be absolute and clean")
		}
	}
	return c, nil
}

// Snapshot authored inputs by content so subsequent edits never change what a
// saved plan or status observes. No credentials are copied into snapshots.
func Load(root string) (*workloadrun.Workflow, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	c, err := ConfigAt(root)
	if err != nil {
		return nil, "", err
	}
	a, err := artifact(c.Artifact)
	if err != nil {
		return nil, "", err
	}
	in, err := workload.ReadIntent(root)
	if err != nil {
		return nil, "", err
	}
	m, err := workload.Resolve(in, true)
	if err != nil {
		return nil, "", fmt.Errorf("authored intent under %s/platform: %w", root, err)
	}
	for _, v := range m.Intent.Workloads {
		if v.Image != a.Reference {
			return nil, "", fmt.Errorf("platform/workloads/%s/%s.yaml image differs from registered artifact; select the matching artifact", v.Project, v.Name)
		}
	}
	snapshot := filepath.Join(root, ".atlas", "inputs", workload.Digest(workload.JSON(m.Intent)))
	for n, b := range m.Authored() {
		path := filepath.Join(snapshot, n)
		if old, e := Read(path, true); e == nil {
			if !bytes.Equal(old, b) {
				return nil, "", errors.New("recorded intent changed")
			}
		} else if os.IsNotExist(e) {
			if e = Write(path, b, true); e != nil {
				return nil, "", e
			}
		} else {
			return nil, "", e
		}
	}
	cfg := workloadrun.Config{Schema: 1, Purpose: "application", InstallationConfig: c.InstallationConfig, InstallationPackage: c.InstallationPackage,
		ProductSource: c.ProductSource, IntentDirectory: snapshot, StateDirectory: filepath.Join(root, ".atlas", "run"), ImageArchive: a.Archive, ImageSHA256: a.SHA256}
	config := filepath.Join(root, ".atlas", "configs", workload.Digest(workload.JSON(cfg))+".json")
	if old, e := Read(config, true); e == nil {
		if !bytes.Equal(old, workload.JSON(cfg)) {
			return nil, "", errors.New("workflow config changed")
		}
	} else if os.IsNotExist(e) {
		if e = Write(config, workload.JSON(cfg), true); e != nil {
			return nil, "", e
		}
	} else {
		return nil, "", e
	}
	w, err := workloadrun.Load(config)
	return w, config, err
}

type Review struct {
	Config string           `json:"config"`
	Plan   workloadrun.Plan `json:"plan"`
}
type ReviewIndex struct {
	Current  string `json:"current"`
	Deployed string `json:"deployed,omitempty"`
}

func readReview(root, digest string) (Review, error) {
	var r Review
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return r, errors.New("invalid workspace review digest")
	}
	b, err := Read(filepath.Join(root, ".atlas/reviews", digest+".json"), true)
	if err != nil {
		return r, err
	}
	if err = workload.StrictDecode(b, &r); err != nil {
		return r, err
	}
	if filepath.Clean(r.Config) != r.Config || workload.Digest(workload.JSON(r.Plan)) != digest || !strings.HasPrefix(r.Config, filepath.Join(root, ".atlas/configs")+string(filepath.Separator)) {
		return r, errors.New("recorded configuration escapes workspace or review digest differs")
	}
	return r, nil
}
func reviewIndex(root string) (ReviewIndex, error) {
	var index ReviewIndex
	b, err := Read(filepath.Join(root, ".atlas/current.json"), true)
	if err != nil {
		return index, err
	}
	err = workload.StrictDecode(b, &index)
	return index, err
}
func recordedReview(root string) (Review, string, error) {
	index, err := reviewIndex(root)
	if err != nil {
		return Review{}, "", err
	}
	current, err := readReview(root, index.Current)
	if err != nil {
		return current, "", err
	}
	entries, err := os.ReadDir(filepath.Join(root, ".atlas/run/authority", index.Current))
	if err != nil && !os.IsNotExist(err) {
		return current, "", err
	}
	if len(entries) == 0 && index.Deployed != "" {
		deployed, err := readReview(root, index.Deployed)
		return deployed, index.Current, err
	}
	return current, "", nil
}
func Recorded(root string) (*workloadrun.Workflow, error) {
	review, draft, err := recordedReview(root)
	if err != nil {
		return nil, fmt.Errorf("recorded plan unavailable; run app plan if no attempt exists: %w", err)
	}
	w, err := workloadrun.Load(review.Config)
	if err != nil {
		return nil, err
	}
	view, err := w.ObservationView(review.Plan)
	if err != nil {
		return nil, err
	}
	view.UnpublishedPlan = draft
	return view, nil
}
func Record(root, config string, p workloadrun.Plan) error {
	digest := workload.Digest(workload.JSON(p))
	index, err := reviewIndex(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && index.Current != digest {
		// Only an existing source-bound final can become the previous deployment.
		raw, e := Read(filepath.Join(root, ".atlas/run/authority", index.Current, "final.json"), true)
		if e == nil {
			var f map[string]any
			if e = workload.StrictDecode(raw, &f); e != nil {
				return e
			}
			if f["planSHA256"] != index.Current || f["result"] != "DEPLOYED" {
				return errors.New("previous completion binding differs")
			}
			index.Deployed = index.Current
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	review := workload.JSON(Review{config, p})
	path := filepath.Join(root, ".atlas/reviews", digest+".json")
	if old, e := Read(path, true); e == nil {
		if !bytes.Equal(old, review) {
			return errors.New("immutable review changed")
		}
	} else if os.IsNotExist(e) {
		if e = Write(path, review, true); e != nil {
			return e
		}
	} else {
		return e
	}
	index.Current = digest
	return Write(filepath.Join(root, ".atlas/current.json"), workload.JSON(index), false)
}

// Selecting an image changes authored intent and its registered artifact only.
// Runtime state, credentials and frozen plans remain untouched.
func SelectArtifact(root, path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	a, err := artifact(path)
	if err != nil {
		return err
	}
	c, err := ConfigAt(root)
	if err != nil {
		return err
	}
	in, err := workload.ReadIntent(root)
	if err != nil {
		return err
	}
	for i := range in.Workloads {
		in.Workloads[i].Image = a.Reference
	}
	m, err := workload.Resolve(in, true)
	if err != nil {
		return err
	}
	human, err := m.HumanAuthored()
	if err != nil {
		return err
	}
	for n, b := range human {
		if err = writeFile(filepath.Join(root, n), b, false, false); err != nil {
			return err
		}
	}
	c.Artifact = path
	return writeConfig(filepath.Join(root, "app.yaml"), c, false)
}

// Public D1 instructions extract directly into the instance directory. Keep
// the historical nested package/ layout usable without assuming author state.
func instancePaths(instance string) (string, string, error) {
	config, err := authored.Resolve(filepath.Join(instance, "installation.yaml"))
	if err != nil {
		return "", "", err
	}
	if _, err := readAuthored(config, false); err != nil {
		return "", "", err
	}
	packageDir := ""
	for _, dir := range []string{instance, filepath.Join(instance, "package")} {
		if _, err := Read(filepath.Join(dir, "runtime.json"), false); err == nil {
			if packageDir != "" {
				return "", "", errors.New("ambiguous D1 package layout; retain one verified package at the instance root or package/")
			}
			packageDir = dir
		} else if !os.IsNotExist(err) {
			return "", "", err
		}
	}
	if packageDir == "" {
		return "", "", errors.New("D1 runtime.json missing at instance root or package/")
	}
	return config, packageDir, nil
}

func readAuthored(path string, private bool) ([]byte, error) {
	p, err := authored.Resolve(path)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(p) != ".yaml" {
		return nil, errors.New("user configuration must use .yaml; JSON is reserved for machine records")
	}
	b, err := Read(p, private)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(p) == ".yaml" {
		return authored.JSON(b)
	}
	return b, nil
}
func writeConfig(path string, c Config, exclusive bool) error {
	b, err := authored.YAML(c)
	if err != nil {
		return err
	}
	return Write(path, b, exclusive)
}
