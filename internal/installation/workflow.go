package installation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

type Record struct {
	Schema            int    `json:"schema"`
	ConfigSHA256      string `json:"configSHA256"`
	ProductSHA256     string `json:"productSHA256"`
	BinarySHA256      string `json:"binarySHA256"`
	InstallID         string `json:"installID"`
	ApprovedPlan      string `json:"approvedPlan,omitempty"`
	BaseCommit        string `json:"baseCommit,omitempty"`
	FullCommit        string `json:"fullCommit,omitempty"`
	ClusterUID        string `json:"clusterUID,omitempty"`
	CertificateSHA256 string `json:"certificateSHA256,omitempty"`
	CredentialsSHA256 string `json:"credentialsSHA256,omitempty"`
	SealedSHA256      string `json:"sealedSHA256,omitempty"`
	Complete          bool   `json:"complete"`
}
type Workflow struct {
	Config        Config
	Product       Product
	ProductDigest string
	BinaryDigest  string
	Record        Record
	Progress      func(string)
	applications  map[string]map[string]string
	runCommand    func(context.Context, string, []byte, string, ...string) ([]byte, error)
}

func (w *Workflow) message(s string) {
	if w.Progress != nil {
		w.Progress(s)
	}
}
func (w *Workflow) runtimeDir() string { return filepath.Join(w.Config.StateDirectory, "runtime") }
func (w *Workflow) repoDir() string    { return filepath.Join(w.Config.StateDirectory, "deployment") }
func (w *Workflow) recordPath() string {
	return filepath.Join(w.Config.StateDirectory, "installation.json")
}
func (w *Workflow) saveRecord() error { return save(w.recordPath(), JSON(w.Record), false) }
func (w *Workflow) Open(create bool) error {
	if e := w.Config.Validate(); e != nil {
		return e
	}
	if e := w.Product.Validate(); e != nil {
		return e
	}
	if Digest(JSON(w.Product)) != w.ProductDigest || !sha.MatchString(w.BinaryDigest) {
		return errors.New("product digest changed")
	}
	if create {
		if e := privateDir(w.Config.StateDirectory); e != nil {
			return e
		}
	}
	b, e := privateRead(w.recordPath())
	if e == nil {
		if e = Decode(b, &w.Record); e != nil {
			return e
		}
		if w.Record.Schema != 1 || w.Record.ConfigSHA256 != Digest(JSON(w.Config)) || w.Record.ProductSHA256 != w.ProductDigest || w.Record.BinarySHA256 != w.BinaryDigest || len(w.Record.InstallID) != 32 {
			return errors.New("installation binding changed; upgrade/adoption is not supported")
		}
		return w.validateRecord()
	}
	if !os.IsNotExist(e) || !create {
		return e
	}
	id := make([]byte, 16)
	if _, e = rand.Read(id); e != nil {
		return e
	}
	w.Record = Record{Schema: 1, ConfigSHA256: Digest(JSON(w.Config)), ProductSHA256: w.ProductDigest, BinarySHA256: w.BinaryDigest, InstallID: hex.EncodeToString(id)}
	return save(w.recordPath(), JSON(w.Record), true)
}

// The OS releases flock on exit, including SIGKILL. The file is a concurrency
// primitive, never a STOP archive or an authority/resume decision.
func (w *Workflow) Lock() (func(), error)     { return w.lock(false) }
func (w *Workflow) ReadLock() (func(), error) { return w.lock(true) }
func (w *Workflow) lock(shared bool) (func(), error) {
	lockPath := filepath.Join(w.Config.StateDirectory, "run.lock")
	if st, e := os.Lstat(lockPath); e == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return nil, errors.New("unsafe installer lock")
	} else if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	f, e := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	mode := syscall.LOCK_EX
	if shared {
		mode = syscall.LOCK_SH
	}
	if e = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("another installer process is active")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func (w *Workflow) plan() any {
	return struct {
		Schema        int      `json:"schema"`
		ProductSHA256 string   `json:"productSHA256"`
		BinarySHA256  string   `json:"binarySHA256"`
		InstallID     string   `json:"installID"`
		Config        Config   `json:"config"`
		Operations    []string `json:"operations"`
	}{1, w.ProductDigest, w.BinaryDigest, w.Record.InstallID, w.Config, []string{
		"Publish only generated gitops/ files to an initially absent branch; every later commit is a direct child, protected by an exact-parent lease",
		"Create four new Kind nodes in OrbStack with loopback-only ingress; no existing cluster adoption or deletion",
		"Instantiate Cilium/Argo Seed and one External Root using finite Bootstrap authority",
		"Back up this instance's Sealed Secrets key to the declared directory and verify readback plus local seal/unseal",
		"Generate three private credentials and publish exactly three strict namespace/name SealedSecrets with consumers",
		"Notify bounded Argo Applications after publication; never change tracking or reopen Seed authority",
		"Verify Web TLS/PVC, Grafana/Prometheus/Alertmanager and S3 positive/negative access; use temporary localhost access",
		"Retain final and Trust Root evidence; do not modify hosts, system trust or default kubeconfig",
	}}
}
func (w *Workflow) PlanJSON() []byte { return JSON(w.plan()) }
func (w *Workflow) app(ctx context.Context, deployment string) (*atlas.App, error) {
	files, e := w.Product.Project(w.Config, false)
	if e != nil {
		return nil, e
	}
	root := w.runtimeDir()
	if e = privateDir(root); e != nil {
		return nil, e
	}
	for path, b := range w.Product.Assets {
		if e = save(filepath.Join(root, path), b, true); e != nil {
			return nil, e
		}
	}
	runner := installRunner{runtime: atlas.ExecRunner{Root: root, ToolDir: ToolDirectory(w.Config.StateDirectory, w.Product.Tools), DockerContext: "orbstack"}, repo: w.repoDir()}
	c := atlas.Config{Schema: 4, Cluster: w.Config.Cluster, RepositoryURL: w.Config.Repository, Revision: w.Config.Branch, GitOpsPath: RootPath, DockerContext: "orbstack", TimeoutSeconds: 1800}
	return atlas.NewInstallation(root, c, w.Product.Lock, runner, atlas.InstallationBinding{HTTPPort: w.Config.HTTPPort, HTTPSPort: w.Config.HTTPSPort, ProductSHA256: w.ProductDigest, BinarySHA256: w.BinaryDigest, InstallID: w.Record.InstallID, DeploymentCommit: deployment, Bundle: files, Images: w.Product.Images})
}

type installRunner struct {
	runtime atlas.ExecRunner
	repo    string
}

func (r installRunner) Run(ctx context.Context, q atlas.Request) ([]byte, error) {
	if q.Tool == "git" {
		r.runtime.Root = r.repo
	}
	return r.runtime.Run(ctx, q)
}
func (w *Workflow) Prepare(ctx context.Context) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("D1 only supports darwin/arm64")
	}
	for _, tool := range []string{"git", "docker", "gh", "orb"} {
		if _, e := exec.LookPath(tool); e != nil {
			return fmt.Errorf("required host prerequisite: %s", tool)
		}
	}
	w.message("Preparing checksum-locked tools in private installation state")
	if e := PrepareTools(ctx, w.Config.StateDirectory, w.Product.Tools); e != nil {
		return e
	}
	a, e := w.app(ctx, "")
	if e != nil {
		return e
	}
	w.message("Preparing digest-locked linux/arm64 image archives; this is the online phase")
	return a.PrepareImages(ctx)
}
func (w *Workflow) CheckPlan(ctx context.Context) error {
	if e := VerifyTools(w.Config.StateDirectory, w.Product.Tools); e != nil {
		return e
	}
	if e := w.checkBackupLocation(); e != nil {
		return e
	}
	probe, e := os.CreateTemp(w.Config.BackupDirectory, ".atlas-write-check-*")
	if e != nil {
		return e
	}
	name := probe.Name()
	e = probe.Close()
	_ = os.Remove(name)
	if e != nil {
		return e
	}
	if e = w.checkGit(ctx); e != nil {
		return e
	}
	a, e := w.app(ctx, w.Record.BaseCommit)
	if e != nil {
		return e
	}
	if e = a.Doctor(ctx); e != nil {
		return e
	}
	if w.Record.BaseCommit == "" {
		for _, port := range []int{w.Config.HTTPPort, w.Config.HTTPSPort} {
			l, e := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
			if e != nil {
				return fmt.Errorf("ingress port %d unavailable: %w", port, e)
			}
			_ = l.Close()
		}
		b, e := a.Runner.Run(ctx, atlas.Request{Tool: "kind", Args: []string{"get", "clusters"}})
		if e != nil {
			return e
		}
		for _, name := range strings.Fields(string(b)) {
			if name == w.Config.Cluster {
				return errors.New("same-name cluster already exists; D1 never adopts it")
			}
		}
	}
	// Capture capacities without inventing an unmeasured acceptance threshold.
	var fs syscall.Statfs_t
	if e = syscall.Statfs(w.Config.StateDirectory, &fs); e != nil {
		return e
	}
	if fs.Bavail == 0 {
		return errors.New("installation disk has no available space")
	}
	return w.captureHost(ctx, uint64(fs.Bavail)*uint64(fs.Bsize), uint64(fs.Blocks)*uint64(fs.Bsize))
}

// A declared external destination must already sit on a distinct mounted
// filesystem. This prevents an absent /Volumes disk from silently becoming a
// same-host directory; the owner still declares the physical isolation class.
func (w *Workflow) checkBackupLocation() error {
	if w.Config.BackupIsolation == "external" {
		parent := w.Config.BackupDirectory
		for {
			_, e := os.Stat(parent)
			if e == nil {
				break
			}
			if !os.IsNotExist(e) {
				return e
			}
			next := filepath.Dir(parent)
			if next == parent {
				return errors.New("external backup volume is absent")
			}
			parent = next
		}
		state, e := os.Stat(w.Config.StateDirectory)
		if e != nil {
			return e
		}
		backup, e := os.Stat(parent)
		if e != nil {
			return e
		}
		a, aok := state.Sys().(*syscall.Stat_t)
		b, bok := backup.Sys().(*syscall.Stat_t)
		if !aok || !bok || a.Dev == b.Dev {
			return errors.New("external backup must be on an already mounted distinct filesystem")
		}
	}
	return privateDir(w.Config.BackupDirectory)
}

func (w *Workflow) validateRecord() error {
	r := w.Record
	for _, v := range []string{r.BaseCommit, r.FullCommit} {
		if v != "" && !commit.MatchString(v) {
			return errors.New("invalid recorded deployment commit")
		}
	}
	if r.CertificateSHA256 != "" && (!sha.MatchString(r.CertificateSHA256) || r.ClusterUID == "" || r.BaseCommit == "") {
		return errors.New("incomplete recorded Trust Root binding")
	}
	if r.CredentialsSHA256 != "" || r.SealedSHA256 != "" {
		if !sha.MatchString(r.CredentialsSHA256) || !sha.MatchString(r.SealedSHA256) || r.CertificateSHA256 == "" {
			return errors.New("incomplete credential binding")
		}
	}
	if r.FullCommit != "" && (r.BaseCommit == "" || r.SealedSHA256 == "" || r.ApprovedPlan != Digest(w.PlanJSON())) {
		return errors.New("full deployment lacks its approved prerequisite record")
	}
	if r.Complete && r.FullCommit == "" {
		return errors.New("completion lacks a full deployment")
	}
	return nil
}
