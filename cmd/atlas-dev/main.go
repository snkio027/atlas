// atlas-dev coordinates a disposable local run. All Tier-0 writes go through
// the existing atlas.App; this command has no Kubernetes repair or delete path.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type workflow struct {
	root, tools, dir, repo string
	configFile             string
	config                 atlas.Config
	app                    *atlas.App
	evidence               map[string]any
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() (result error) {
	if len(os.Args) < 2 || (os.Args[1] != "up" && os.Args[1] != "verify") {
		return errors.New("usage: atlas-dev up|verify --tool-dir <tools> [--approve-cluster <exact name> --approve-tier0 --install-kubeconfig]")
	}
	command := os.Args[1]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	root := f.String("root", ".", "source checkout")
	tools := f.String("tool-dir", "", "locked tools")
	configFile := f.String("config", "profiles/development.json", "reviewed repository-relative profile")
	approved := f.String("approve-cluster", "", "exact creation target")
	tier0 := f.Bool("approve-tier0", false, "approve existing Bootstrap engine's initial Tier-0 writes")
	install := f.Bool("install-kubeconfig", false, "backup and merge validated access into default kubeconfig")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *tools != "" {
		var err error
		*tools, err = filepath.Abs(*tools)
		if err != nil {
			return err
		}
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	abs, e = filepath.EvalSymlinks(abs)
	if e != nil {
		return e
	}
	c, _, e := atlas.Load(abs, *configFile)
	if e != nil {
		return e
	}
	if c.Cluster == developmentprofile.OT1Cluster && *install {
		return errors.New("OT-1 must keep its dedicated kubeconfig; default access installation is forbidden")
	}
	if c.Schema != 3 {
		return errors.New("atlas-dev requires the four-node profile")
	}
	if command == "up" && (*approved != c.Cluster || !*tier0) {
		return errors.New("up requires the exact --approve-cluster and --approve-tier0")
	}
	if os.Getenv("KUBECONFIG") != "" && *install {
		return errors.New("unset KUBECONFIG before installing default access")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	w := workflow{root: abs, tools: *tools, config: c, configFile: *configFile, evidence: map[string]any{"cluster": c.Cluster, "startedAt": time.Now().UTC().Format(time.RFC3339)}}
	w.dir = filepath.Join(abs, ".state/development", c.Cluster)
	w.repo = filepath.Join(w.dir, "repo")
	if e = privateDir(w.dir); e != nil {
		return e
	}
	runLock := filepath.Join(w.dir, "run.lock")
	if e = os.Mkdir(runLock, 0700); e != nil {
		return errors.New("development run lock exists; inspect the previous process before removal")
	}
	defer os.Remove(runLock)
	completed := false
	defer func() {
		// A panic also runs defers; it must never manufacture a PASS.
		if !completed && result == nil {
			result = errors.New("development workflow did not complete")
		}
		result = w.finish(result, os.Stdout)
	}()
	if command == "up" {
		if e = w.checkout(ctx); e != nil {
			return e
		}
	}
	c, l, e := atlas.Load(w.repo, w.configFile)
	if e != nil {
		return e
	}
	w.app = &atlas.App{Root: w.repo, Config: c, Lock: l, Runner: atlas.ExecRunner{Root: w.repo, ToolDir: w.tools, DockerContext: c.DockerContext}}
	if command == "up" {
		fmt.Println("1/6 Preparing locked image archives (online preparation only)")
		if e = w.app.PrepareImages(ctx); e != nil {
			return e
		}
		fmt.Println("2/6 Doctor and deterministic render")
		if e = w.app.Doctor(ctx); e != nil {
			return e
		}
		files, e := w.app.Render(ctx)
		if e != nil {
			return e
		}
		hashes := map[string]string{}
		for path, b := range files {
			h := sha256.Sum256(b)
			hashes[path] = hex.EncodeToString(h[:])
		}
		w.evidence["renderSHA256"] = hashes
		fmt.Println("3/6 Creating four nodes and waiting for GitOps adoption")
		if e = w.app.Apply(ctx, *approved, *tier0); e != nil {
			return e
		}
		w.evidence["firstApplyExitCode"] = 0
	}
	fmt.Println("4/6 Verifying nodes, workloads, ownership, PVC and HTTPS")
	if e = w.verify(ctx); e != nil {
		return e
	}
	if command == "up" {
		fmt.Println("5/6 Repeating apply and checking API audit for zero writes")
		before, e := auditWrites(w.repo)
		if e != nil {
			return e
		}
		beforeIDs, e := w.identities(ctx)
		if e != nil {
			return e
		}
		if e = w.app.Apply(ctx, *approved, *tier0); e != nil {
			return e
		}
		afterIDs, e := w.identities(ctx)
		if e != nil {
			return e
		}
		after, e := auditWrites(w.repo)
		if e != nil {
			return e
		}
		if !maps.Equal(after, before) || string(beforeIDs) != string(afterIDs) {
			return errors.New("repeated apply changed object identities or issued Kubernetes writes")
		}
		w.evidence["repeatApply"] = map[string]any{"exitCode": 0, "kubectlMutationDelta": len(after) - len(before), "identitiesStable": true}
	}
	if *install {
		fmt.Println("6/6 Backing up and installing default kubectl access")
		if e = w.installAccess(ctx); e != nil {
			return e
		}
		w.evidence["defaultContext"] = "kind-" + c.Cluster
	}
	// A read-only verify already sampled the complete runtime once. Recheck
	// only after repeat apply or access installation could have changed state.
	if command == "up" || *install {
		if e = w.verify(ctx); e != nil {
			return e
		}
	}
	completed = true
	return nil
}

func privateDir(path string) error { return checkedDir(path, true) }
func checkedDir(path string, private bool) error {
	// Refuse symlink traversal for local credentials/state, including ancestors.
	for p := path; p != filepath.Dir(p); p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e == nil && s.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in private state path")
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	if e := os.MkdirAll(path, 0700); e != nil {
		return e
	}
	s, e := os.Stat(path)
	if e != nil {
		return e
	}
	if private && s.Mode().Perm()&0077 != 0 {
		return errors.New("private state directory must be owner-only")
	}
	return nil
}
func (w *workflow) exec(ctx context.Context, dir, tool string, env []string, args ...string) ([]byte, error) {
	if w.tools != "" && tool == "kubectl" {
		tool = filepath.Join(w.tools, tool)
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	b, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("%s %s failed; private subprocess output suppressed: %w", filepath.Base(tool), args[0], e)
	}
	return b, nil
}
func (w *workflow) checkout(ctx context.Context) error {
	b, e := w.exec(ctx, w.root, "git", nil, "status", "--porcelain", "--untracked-files=all")
	if e != nil {
		return e
	}
	if len(b) > 0 {
		return errors.New("commit and publish the clean source checkout before up")
	}
	sha, e := w.exec(ctx, w.root, "git", nil, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	commit := strings.TrimSpace(string(sha))
	w.evidence["commit"] = commit
	if _, e = os.Stat(w.repo); os.IsNotExist(e) {
		if _, e = w.exec(ctx, w.root, "git", nil, "clone", "--no-hardlinks", w.root, w.repo); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	b, e = w.exec(ctx, w.repo, "git", nil, "status", "--porcelain", "--untracked-files=all")
	if e != nil {
		return e
	}
	if len(b) > 0 {
		return errors.New("runtime checkout contains changes")
	}
	if _, e = w.exec(ctx, w.repo, "git", nil, "fetch", "--no-tags", w.root, commit); e != nil {
		return e
	}
	_, e = w.exec(ctx, w.repo, "git", nil, "checkout", "--detach", commit)
	return e
}
func (w *workflow) kube(ctx context.Context, args ...string) ([]byte, error) {
	// Use the same fingerprint/permission-checked kubeconfig as atlas.App by
	// checking status before verification; every request still pins path/context.
	cfg := filepath.Join(w.repo, ".state/kubeconfig")
	b, e := os.ReadFile(cfg)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	expected, e := os.ReadFile(cfg + ".sha256")
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(string(expected)) != hex.EncodeToString(h[:]) {
		return nil, errors.New("kubeconfig digest changed")
	}
	return w.exec(ctx, w.repo, "kubectl", nil, append([]string{"--kubeconfig", cfg, "--context", "kind-" + w.config.Cluster, "--request-timeout=30s"}, args...)...)
}
func (w *workflow) identities(ctx context.Context) ([]byte, error) {
	out := map[string]string{}
	for _, item := range []struct{ kind, ns, name string }{{"configmap", "kube-system", "atlas-refactor-identity"}, {"configmap", "kube-system", "atlas-refactor-handoff"}, {"configmap", "kube-system", "atlas-refactor-receipt"}, {"application", "argocd", "atlas-refactor-root"}, {"application", "argocd", "argocd-self"}, {"configmap", "argocd", "atlas-refactor-adoption-signal"}} {
		b, e := w.kube(ctx, "get", item.kind, item.name, "-n", item.ns, "-o", "jsonpath={.metadata.uid}")
		if e != nil {
			return nil, e
		}
		if len(b) == 0 {
			return nil, errors.New("missing identity UID")
		}
		out[item.name] = string(b)
	}
	w.evidence["identities"] = out
	return json.Marshal(out)
}
func (w *workflow) verify(ctx context.Context) error {
	report := w.app.Status(ctx)
	w.evidence["bootstrap"] = report
	if report.State != atlas.Adopted {
		return fmt.Errorf("final Bootstrap state: %s: %s", report.State, report.Detail)
	}
	if e := w.app.VerifyNodes(ctx); e != nil {
		return e
	}
	if e := w.app.VerifyDevelopmentRollout(ctx); e != nil {
		return e
	}
	b, e := w.kube(ctx, "get", "nodes", "-o", "json")
	if e != nil {
		return e
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name   string
				Labels map[string]string
			}
			Status struct {
				Conditions []struct{ Type, Status string }
			}
		}
	}
	if e = json.Unmarshal(b, &nodes); e != nil {
		return e
	}
	w.evidence["nodes"] = nodes.Items
	b, e = w.kube(ctx, "get", "pods", "-A", "-o", "json")
	if e != nil {
		return e
	}
	var pods struct{ Items []runtimePod }
	if e = json.Unmarshal(b, &pods); e != nil {
		return e
	}
	if e = verifyPods(pods.Items, w.config.Cluster); e != nil {
		return e
	}
	w.evidence["pods"] = pods.Items
	b, e = w.kube(ctx, "get", "applications", "-n", "argocd", "-o", "json")
	if e != nil {
		return e
	}
	var apps struct {
		Items []struct {
			Metadata struct{ Name, UID string }
			Status   struct {
				Sync   struct{ Status, Revision string }
				Health struct{ Status string }
			}
		}
	}
	if e = json.Unmarshal(b, &apps); e != nil {
		return e
	}
	w.evidence["applications"] = apps.Items
	b, e = w.kube(ctx, "get", "pvc", "web-data", "-n", "workload-web", "-o", "json")
	if e != nil {
		return e
	}
	var pvc struct {
		Metadata struct{ Name, UID string }
		Spec     struct{ VolumeName, StorageClassName string }
		Status   struct{ Phase string }
	}
	if e = json.Unmarshal(b, &pvc); e != nil {
		return e
	}
	if pvc.Status.Phase != "Bound" || pvc.Spec.VolumeName == "" {
		return errors.New("PVC not Bound")
	}
	w.evidence["pvc"] = pvc
	b, e = w.kube(ctx, "get", "pv", pvc.Spec.VolumeName, "-o", "json")
	if e != nil {
		return e
	}
	var pv struct {
		Metadata struct{ Name, UID string }
		Spec     struct {
			PersistentVolumeReclaimPolicy string
			NodeAffinity                  any
		}
	}
	if e = json.Unmarshal(b, &pv); e != nil {
		return e
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != "Retain" {
		return errors.New("PV reclaim policy drift")
	}
	w.evidence["pv"] = pv
	ca, e := w.kube(ctx, "get", "secret", "development-ca", "-n", "atlas-gateway", "-o", `jsonpath={.data.tls\.crt}`)
	if e != nil {
		return e
	}
	pem, e := base64.StdEncoding.DecodeString(string(ca))
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("invalid public development CA")
	}
	if e = os.WriteFile(filepath.Join(w.dir, "development-ca.crt"), pem, 0600); e != nil {
		return e
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
	}}}
	defer client.CloseIdleConnections()
	profile, e := developmentprofile.Lookup(w.config.Revision)
	if e != nil {
		return e
	}
	for _, scheme := range []string{"http", "https"} {
		port := strconv.Itoa(profile.HTTPPort)
		if scheme == "https" {
			port = strconv.Itoa(profile.HTTPSPort)
		}
		req, e := http.NewRequestWithContext(ctx, "GET", scheme+"://web.atlas.test:"+port+"/", nil)
		if e != nil {
			return e
		}
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if e != nil {
			return e
		}
		if scheme == "http" {
			if resp.StatusCode != 301 || resp.Header.Get("Location") != "https://web.atlas.test:"+strconv.Itoa(profile.HTTPSPort)+"/" {
				return errors.New("HTTP redirect validation failed")
			}
		} else if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "Atlas development web:") {
			return errors.New("HTTPS application response validation failed")
		}
		w.evidence[scheme] = map[string]any{"status": resp.StatusCode, "location": resp.Header.Get("Location"), "body": string(body), "tlsVerified": scheme == "https"}
	}
	_, e = w.identities(ctx)
	return e
}
func (w *workflow) installAccess(ctx context.Context) error {
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	dir := filepath.Join(home, ".kube")
	if e = checkedDir(dir, false); e != nil {
		return e
	}
	path := filepath.Join(dir, "config")
	if s, e := os.Lstat(path); e == nil {
		if !s.Mode().IsRegular() {
			return errors.New("default kubeconfig must be a regular file")
		}
		old, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		backup := filepath.Join(dir, "config.before-atlas-"+time.Now().UTC().Format("20060102T150405.000000000"))
		if e = os.WriteFile(backup, old, 0600); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	cfg := filepath.Join(w.repo, ".state/kubeconfig")
	merged, e := w.exec(ctx, w.repo, "kubectl", []string{"KUBECONFIG=" + cfg + string(os.PathListSeparator) + path}, "config", "view", "--flatten", "--raw", "-o", "json")
	if e != nil {
		return e
	}
	var config map[string]any
	if e = json.Unmarshal(merged, &config); e != nil {
		return e
	}
	config["current-context"] = "kind-" + w.config.Cluster
	tmp, e := os.CreateTemp(dir, ".atlas-config-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Close()
	if e = jsonFile(name, config); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	b, e := w.exec(ctx, w.repo, "kubectl", nil, "--request-timeout=15s", "get", "nodes", "-o", "json")
	if e != nil {
		return e
	}
	var list struct {
		Items []struct{ Metadata struct{ Name string } }
	}
	if e = json.Unmarshal(b, &list); e != nil {
		return e
	}
	if len(list.Items) != 4 {
		return errors.New("default kubectl did not return four nodes")
	}
	return nil
}
