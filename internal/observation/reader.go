package observation

import (
	"context"
	"errors"
	"fmt"
	kubeconfigtransport "github.com/snkio027/atlas/internal/kubeconfig"
	"github.com/snkio027/atlas/internal/platform"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Reader deliberately exposes only read operations. Ceremony never receives a
// generic Kubernetes executor through the observation package.
type Reader interface {
	Read(context.Context, Ref) (Object, error)
	ClusterIdentity(context.Context) (string, error)
}
type APIReader struct {
	target     Target
	kubeconfig string
	server     string
	client     *http.Client
	model      *platform.ResourceModel
	discovery  map[string]map[string]string
}

// RegularPrivate rejects symlinks at all path components and loose permissions.
func RegularPrivate(path string) ([]byte, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symlink in private path")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	info, e := os.Stat(abs)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private file must be a regular owner-only file")
	}
	return os.ReadFile(abs)
}
func command(ctx context.Context, tool string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "SYSTEMROOT"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	// config view is purely local. Never expose its raw client-key output/errors.
	b, e := cmd.Output()
	if e != nil {
		return nil, errors.New("local kubectl configuration command failed; output suppressed")
	}
	return b, nil
}
func NewAPIReader(ctx context.Context, kubeconfig, kubectl, kubectlVersion, kubernetesVersion string, target Target, model *platform.ResourceModel) (*APIReader, error) {
	if e := target.Validate(); e != nil {
		return nil, e
	}
	if model == nil {
		return nil, errors.New("locked resource model is required")
	}
	raw, e := RegularPrivate(kubeconfig)
	if e != nil {
		return nil, e
	}
	if SHA(raw) != target.KubeconfigSHA256 {
		return nil, errors.New("kubeconfig binding mismatch")
	}
	version, e := command(ctx, kubectl, "version", "--client", "-o", "json")
	if e != nil {
		return nil, e
	}
	var cv Object
	if e = Decode(version, &cv, false); e != nil {
		return nil, e
	}
	if String(At(cv, "clientVersion", "gitVersion")) != "v"+kubectlVersion {
		return nil, errors.New("kubectl version differs from lock")
	}
	b, e := command(ctx, kubectl, "--kubeconfig", kubeconfig, "--context", target.Context, "config", "view", "--raw", "--minify", "-o", "json")
	if e != nil {
		return nil, e
	}
	var config kubeconfigtransport.Projection
	if e = Decode(b, &config, false); e != nil {
		return nil, errors.New("invalid kubeconfig projection")
	}
	server, transport, e := kubeconfigtransport.Transport(config, target.Context)
	if e != nil {
		return nil, e
	}
	r := &APIReader{target: target, kubeconfig: kubeconfig, server: server, model: model, discovery: map[string]map[string]string{}, client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("API redirect forbidden") }}}
	v, missing, e := r.get(ctx, "/version")
	if e != nil || missing || String(v["gitVersion"]) != "v"+kubernetesVersion {
		return nil, errors.New("API version does not match locked Kubernetes")
	}
	uid, e := r.ClusterIdentity(ctx)
	if e != nil || uid != target.ClusterUID {
		return nil, errors.New("cluster identity differs from explicit binding")
	}
	return r, nil
}
func (r *APIReader) Close() { r.client.CloseIdleConnections() }
func (r *APIReader) get(ctx context.Context, path string) (Object, bool, error) {
	raw, e := RegularPrivate(r.kubeconfig)
	if e != nil || SHA(raw) != r.target.KubeconfigSHA256 {
		return nil, false, errors.New("kubeconfig changed")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, r.server+path, nil)
	if e != nil {
		return nil, false, e
	}
	req.Header.Set("Accept", "application/json")
	response, e := r.client.Do(req)
	if e != nil {
		return nil, false, errors.New("API read unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, true, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("API read rejected (%d); body suppressed", response.StatusCode)
	}
	const limit = 32 << 20
	data, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if e != nil || len(data) > limit {
		return nil, false, errors.New("API response unavailable/too large")
	}
	var obj Object
	if e = Decode(data, &obj, false); e != nil {
		return nil, false, errors.New("invalid API JSON")
	}
	return obj, false, nil
}
func (r *APIReader) resourcePath(ctx context.Context, ref Ref) (string, error) {
	if e := ref.Validate(); e != nil {
		return "", e
	}
	if ref.Kind == "Secret" || ref.Kind == "SealedSecret" {
		return "", errors.New("credential reads are outside Observation")
	}
	entry, e := r.model.Resolve(Object{"apiVersion": ref.APIVersion, "kind": ref.Kind})
	if e != nil {
		return "", e
	}
	if entry.Scope == platform.ClusterScope && ref.Namespace != "" || entry.Scope == platform.NamespacedScope && ref.Namespace == "" {
		return "", errors.New("namespace disagrees with locked resource scope")
	}
	prefix := "/api/" + ref.APIVersion
	if strings.Contains(ref.APIVersion, "/") {
		prefix = "/apis/" + ref.APIVersion
	}
	if r.discovery[ref.APIVersion] == nil {
		document, missing, e := r.get(ctx, prefix)
		if e != nil || missing {
			return "", errors.New("API discovery unavailable")
		}
		if String(document["kind"]) != "APIResourceList" || String(document["groupVersion"]) != ref.APIVersion {
			return "", errors.New("API discovery identity mismatch")
		}
		names := map[string]string{}
		for _, raw := range Slice(document["resources"]) {
			resource := Map(raw)
			name := String(resource["name"])
			kind := String(resource["kind"])
			if strings.Contains(name, "/") {
				continue
			}
			if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(name) {
				return "", errors.New("invalid API resource name")
			}
			namespaced, ok := resource["namespaced"].(bool)
			if !ok {
				return "", errors.New("API scope missing")
			}
			declared, e := r.model.Resolve(Object{"apiVersion": ref.APIVersion, "kind": kind})
			if e != nil {
				continue
			}
			if namespaced != (declared.Scope == platform.NamespacedScope) {
				return "", errors.New("API scope differs from locked model")
			}
			canGet := false
			for _, verb := range Slice(resource["verbs"]) {
				canGet = canGet || verb == "get"
			}
			if !canGet {
				continue
			}
			if names[kind] != "" {
				return "", errors.New("ambiguous kind discovery")
			}
			names[kind] = name
		}
		r.discovery[ref.APIVersion] = names
	}
	plural := r.discovery[ref.APIVersion][ref.Kind]
	if plural == "" {
		return "", errors.New("expected GVK is not served/readable")
	}
	if ref.Namespace != "" {
		prefix += "/namespaces/" + ref.Namespace
	}
	return prefix + "/" + plural + "/" + ref.Name, nil
}
func (r *APIReader) Read(ctx context.Context, ref Ref) (Object, error) {
	path, e := r.resourcePath(ctx, ref)
	if e != nil {
		return nil, e
	}
	o, missing, e := r.get(ctx, path)
	if e != nil || missing {
		return nil, e
	}
	if Reference(o) != ref {
		return nil, errors.New("API response identity mismatch")
	}
	return o, nil
}
func (r *APIReader) ClusterIdentity(ctx context.Context) (string, error) {
	o, e := r.Read(ctx, Ref{"v1", "Namespace", "", "kube-system"})
	if e != nil || o == nil {
		return "", errors.New("cluster identity unavailable")
	}
	uid := String(At(o, "metadata", "uid"))
	if uid == "" {
		return "", errors.New("cluster UID missing")
	}
	return uid, nil
}

// Assert at compile time; no Run/Apply/Patch method is exposed by this API.
var _ Reader = (*APIReader)(nil)

// InventoryReader adds bounded namespace inventory reads without granting writes.
// Collection refuses pagination rather than mistaking an incomplete list for absence.
type InventoryReader interface {
	Reader
	List(context.Context, Ref) ([]Object, error)
}

func (r *APIReader) List(ctx context.Context, kind Ref) ([]Object, error) {
	if kind.Name != "" {
		return nil, errors.New("inventory must not select a name")
	}
	probe := kind
	probe.Name = "observation-inventory"
	path, e := r.resourcePath(ctx, probe)
	if e != nil {
		return nil, e
	}
	path = strings.TrimSuffix(path, "/"+probe.Name)
	o, missing, e := r.get(ctx, path)
	if e != nil || missing {
		return nil, errors.New("inventory unavailable")
	}
	items, ok := o["items"].([]any)
	if !ok || String(o["apiVersion"]) != kind.APIVersion || String(o["kind"]) != kind.Kind+"List" || String(At(o, "metadata", "resourceVersion")) == "" {
		return nil, errors.New("incomplete inventory response")
	}
	if continuation, present := Map(o["metadata"])["continue"]; present {
		if token, ok := continuation.(string); !ok || token != "" {
			return nil, errors.New("incomplete inventory response")
		}
	}
	out := []Object{}
	seen := map[string]bool{}
	for _, raw := range items {
		item := Map(raw)
		if item == nil {
			return nil, errors.New("invalid inventory member")
		}
		// Typed Kubernetes lists can omit member TypeMeta. Inherit only absent
		// fields after discovery, locked scope and the list header are verified.
		// Explicit null, empty, malformed or conflicting values still fail closed.
		for field, expected := range map[string]string{"apiVersion": kind.APIVersion, "kind": kind.Kind} {
			if value, present := item[field]; present {
				if text, ok := value.(string); !ok || text != expected {
					return nil, errors.New("invalid inventory member type")
				}
			} else {
				item[field] = expected
			}
		}
		ref := Reference(item)
		if ref.APIVersion != kind.APIVersion || ref.Kind != kind.Kind || ref.Namespace != kind.Namespace || ref.Validate() != nil || seen[ref.Key()] {
			return nil, errors.New("invalid inventory member")
		}
		seen[ref.Key()] = true
		out = append(out, item)
	}
	return out, nil
}
