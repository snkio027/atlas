// Package platform renders the development GitOps candidate. It has no cluster
// client, apply operation, network fetcher, or Bootstrap mutation authority.
package platform

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/developmentprofile"
	"atlas-refactor/internal/platformnames"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Object = map[string]any

type Config struct {
	Schema        int               `json:"schema"`
	RepositoryURL string            `json:"repositoryURL"`
	Revision      string            `json:"revision"`
	RootPath      string            `json:"rootPath"`
	Components    map[string]string `json:"components"`
}
type Artifact struct {
	SHA256       string `json:"sha256"`
	Source       string `json:"source"`
	Verification string `json:"verification"`
}
type Lock struct {
	Schema     int                 `json:"schema"`
	Helm       string              `json:"helm"`
	Kubectl    string              `json:"kubectl"`
	YQ         string              `json:"yq"`
	Kubernetes string              `json:"kubernetes"`
	Images     map[string]string   `json:"images"`
	Artifacts  map[string]Artifact `json:"artifacts"`
}
type Tools struct{ Helm, Kubectl, YQ string }
type Project struct {
	Root         string
	Config       Config
	Lock         Lock
	Tools        Tools
	Capabilities *Capabilities
}

const inputDir = "platform/development"

func readJSON(path string, v any) error {
	b, e := readInput(path)
	if e != nil {
		return e
	}
	if e := uniqueJSONKeys(b); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
func Load(root string, t Tools) (*Project, error) {
	p := &Project{Root: root, Tools: t}
	if e := readJSON(filepath.Join(root, inputDir, "config.json"), &p.Config); e != nil {
		return nil, e
	}
	if e := readJSON(filepath.Join(root, inputDir, "versions.lock.json"), &p.Lock); e != nil {
		return nil, e
	}
	if (p.Config.Schema != 1 && p.Config.Schema != 2) || p.Lock.Schema != 1 {
		return nil, errors.New("unsupported platform schema")
	}
	if p.Config.RootPath != "gitops/root/overlays/development" || p.Config.RepositoryURL != "https://github.com/snkio027/atlas-refactor.git" || !reviewedDevelopmentRevision(p.Config.Revision) {
		return nil, errors.New("unreviewed development target")
	}
	expected := map[string]string{"foundation": "foundation", "cilium": "networking/cilium", "argocd-self": "management/argocd-self", "cert-manager": "management/cert-manager", "gateway-api": "networking/gateway-api", "envoy-crds": "networking/envoy-crds", "envoy-gateway": "networking/envoy-gateway", "local-storage": "storage/local-path", "local-pki": "management/local-pki", "edge": "networking/edge"}
	if len(expected) != len(p.Config.Components) {
		return nil, errors.New("unexpected capability set")
	}
	for n, path := range expected {
		if p.Config.Components[n] != "gitops/platform/"+path+"/overlays/development" {
			return nil, fmt.Errorf("unreviewed component path: %s", n)
		}
	}
	if t.Helm == "" || t.Kubectl == "" || t.YQ == "" {
		return nil, errors.New("helm, kubectl and yq executables are required")
	}
	if e := p.loadCapabilities(); e != nil {
		return nil, e
	}
	if _, e := p.resourceModel(nil); e != nil {
		return nil, e
	}
	return p, nil
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Select one versioned input from the checksum-verified lock, not from the
// filesystem or a latest-version search. Frozen experiments retain their locks.
func (p *Project) artifactPath(prefix, suffix string) (string, error) {
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `[0-9]+\.[0-9]+\.[0-9]+` + regexp.QuoteMeta(suffix) + "$")
	selected := ""
	for path := range p.Lock.Artifacts {
		if !pattern.MatchString(path) {
			continue
		}
		if selected != "" {
			return "", fmt.Errorf("ambiguous locked input: %s*%s", prefix, suffix)
		}
		selected = path
	}
	if selected == "" {
		return "", fmt.Errorf("missing versioned locked input: %s*%s", prefix, suffix)
	}
	return selected, nil
}

func (p *Project) VerifyArtifacts() error {
	if len(p.Lock.Artifacts) < 9 {
		return errors.New("incomplete artifact lock")
	}
	for path, a := range p.Lock.Artifacts {
		if filepath.IsAbs(path) || strings.Contains(path, "..") || !(strings.HasPrefix(path, "vendor/") || strings.HasPrefix(path, "assets/")) {
			return fmt.Errorf("unsafe artifact path: %s", path)
		}
		b, e := readInput(filepath.Join(p.Root, path))
		if e != nil {
			return e
		}
		if hash(b) != a.SHA256 {
			return fmt.Errorf("artifact checksum mismatch: %s", path)
		}
	}
	for n, im := range p.Lock.Images {
		if !pinnedImage.MatchString(im) {
			return fmt.Errorf("image is not version/digest pinned: %s", n)
		}
	}
	return nil
}
func (p *Project) run(ctx context.Context, input []byte, tool string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = p.Root
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", filepath.Base(tool), args, e, stderr.String())
	}
	return b, nil
}
func (p *Project) verifyTools(ctx context.Context) error {
	for _, check := range []struct {
		tool string
		args []string
		want string
	}{{p.Tools.Helm, []string{"version", "--template", "{{.Version}}"}, "v" + p.Lock.Helm}, {p.Tools.YQ, []string{"--version"}, "yq (https://github.com/mikefarah/yq/) version v" + p.Lock.YQ}} {
		b, e := p.run(ctx, nil, check.tool, check.args...)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(b)) != check.want {
			return fmt.Errorf("unexpected %s version: %s", check.tool, b)
		}
	}
	b, e := p.run(ctx, nil, p.Tools.Kubectl, "version", "--client", "-o", "json")
	if e != nil {
		return e
	}
	var v struct {
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return e
	}
	if v.ClientVersion.GitVersion != "v"+p.Lock.Kubectl {
		return errors.New("kubectl version mismatch")
	}
	return nil
}
func decodeObjects(b []byte) ([]Object, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	var out []Object
	for {
		var o Object
		e := d.Decode(&o)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if o == nil {
			continue
		}
		if o["kind"] == "List" {
			for _, v := range slice(o["items"]) {
				out = append(out, mapping(v))
			}
		} else {
			out = append(out, o)
		}
	}
	return out, nil
}
func (p *Project) yaml(ctx context.Context, b []byte) ([]Object, error) {
	out, e := p.run(ctx, b, p.Tools.YQ, "eval", "-o=json", "-I=0", ".", "-")
	if e != nil {
		return nil, e
	}
	return decodeObjects(out)
}
func encoded(objects []Object) []byte {
	var out bytes.Buffer
	for i, o := range objects {
		if i > 0 {
			out.WriteString("---\n")
		}
		b, _ := json.MarshalIndent(o, "", "  ")
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
func (p *Project) Render(ctx context.Context) (map[string][]byte, error) {
	if e := p.ValidateCapabilitySelection(p.Capabilities.Active); e != nil {
		return nil, e
	}
	if e := p.VerifyArtifacts(); e != nil {
		return nil, e
	}
	if e := p.verifyTools(ctx); e != nil {
		return nil, e
	}
	files := map[string][]byte{}
	jobs := []struct{ name, release, chartPrefix, ns, values string }{
		{"cilium", "cilium", "vendor/platform/cilium-", "kube-system", inputDir + "/values/cilium.json"},
		{"cert-manager", "cert-manager", "vendor/platform/cert-manager-v", "cert-manager", inputDir + "/values/cert-manager.json"},
		{"envoy-gateway", "eg", "vendor/platform/gateway-helm-v", "envoy-gateway-system", inputDir + "/values/envoy-gateway.json"},
		{"argocd-self", "atlas-refactor-argocd", "vendor/charts/argo-cd-", "argocd", "assets/argocd-values.yaml"},
	}
	for _, job := range jobs {
		if p.currentLayout() && job.name != "cilium" {
			if job.name == "envoy-gateway" {
				job.ns = platformnames.Gateway
			} else if job.name == "argocd-self" {
				job.ns = platformnames.Argo
			} else {
				job.ns = platformnames.CertManager
			}
		}
		chart, e := p.artifactPath(job.chartPrefix, ".tgz")
		if e != nil {
			return nil, e
		}
		args := []string{"template", job.release, chart, "--namespace", job.ns, "--include-crds", "--skip-tests", "--kube-version", p.Lock.Kubernetes, "--values", job.values}
		valuePath, e := p.inputPath(job.values)
		if e != nil {
			return nil, e
		}
		args[len(args)-1] = valuePath
		if job.name == "argocd-self" {
			args = append(args, "--set", "configs.secret.createSecret=false")
		}
		b, e := p.helmTemplate(ctx, args)
		if e != nil {
			return nil, e
		}
		objs, e := p.yaml(ctx, b)
		if e != nil {
			return nil, e
		}
		if job.name == "argocd-self" {
			cmBytes, e := readInput(filepath.Join(p.Root, "assets/argocd-cm.yaml"))
			if e != nil {
				return nil, e
			}
			cms, e := p.yaml(ctx, cmBytes)
			if e != nil {
				return nil, e
			}
			health, e := readInput(filepath.Join(p.Root, inputDir, "health/ready.lua"))
			if e != nil {
				return nil, e
			}
			data := mapping(cms[0]["data"])
			data["application.resourceTrackingMethod"] = "annotation"
			data["resource.ignoreResourceUpdatesEnabled"] = "false"
			for _, k := range []string{"cert-manager.io_Issuer", "cert-manager.io_Certificate", "gateway.networking.k8s.io_GatewayClass", "gateway.networking.k8s.io_Gateway", "gateway.networking.k8s.io_HTTPRoute", "gateway.envoyproxy.io_BackendTrafficPolicy"} {
				data["resource.customizations.health."+k] = string(health)
			}
			// Frozen S1 catalogs have no data operators. Do not introduce new
			// inputs or Seed bytes into those historical projections.
			if p.hasDataOperators() {
				operatorHealth, e := readInput(filepath.Join(p.Root, inputDir, "health/data-operators.lua"))
				if e != nil {
					return nil, e
				}
				for _, k := range []string{"postgresql.cnpg.io_Cluster", "kafka.strimzi.io_Kafka", "kafka.strimzi.io_KafkaUser", "flink.apache.org_FlinkDeployment"} {
					data["resource.customizations.health."+k] = string(operatorHealth)
				}
			}
			if p.currentLayout() {
				metadata(cms[0])["namespace"] = platformnames.Argo
				objs = append(objs, namespaceObject(platformnames.Get("argocd")))
			}
			objs = append(objs, cms[0])
		}
		for _, o := range objs {
			if o["kind"] == "Deployment" || o["kind"] == "StatefulSet" || o["kind"] == "Job" {
				pod := mapping(mapping(o["spec"])["template"])["spec"].(map[string]any)
				pod["nodeSelector"] = Object{"kubernetes.io/os": "linux", "node-role.local/compute": "true"}
			}
		}
		files[p.Config.Components[job.name]+"/rendered.yaml"] = encoded(objs)
		if job.name == "cilium" || job.name == "argocd-self" {
			files[inputDir+"/bootstrap/"+job.name+"-seed.yaml"] = encoded(objs)
		}
	}
	for _, job := range []struct{ name, prefix, suffix string }{{"gateway-api", "vendor/platform/gateway-api-v", "-standard.yaml"}, {"envoy-crds", "vendor/platform/envoy-gateway-v", "-install.yaml"}} {
		path, e := p.artifactPath(job.prefix, job.suffix)
		if e != nil {
			return nil, e
		}
		b, e := readInput(filepath.Join(p.Root, path))
		if e != nil {
			return nil, e
		}
		objs, e := p.yaml(ctx, b)
		if e != nil {
			return nil, e
		}
		if job.name == "envoy-crds" {
			var filtered []Object
			for _, o := range objs {
				if o["kind"] == "CustomResourceDefinition" && mapping(o["spec"])["group"] == "gateway.envoyproxy.io" {
					filtered = append(filtered, o)
				}
			}
			objs = filtered
		}
		if len(objs) == 0 {
			return nil, fmt.Errorf("empty %s artifact", job.name)
		}
		for _, o := range objs {
			if o["kind"] == "Deployment" || o["kind"] == "StatefulSet" || o["kind"] == "Job" {
				pod := mapping(mapping(o["spec"])["template"])["spec"].(map[string]any)
				pod["nodeSelector"] = Object{"kubernetes.io/os": "linux", "node-role.local/compute": "true"}
			}
		}
		files[p.Config.Components[job.name]+"/rendered.yaml"] = encoded(objs)
	}
	b, e := readInput(filepath.Join(p.Root, "vendor/platform/kind-storage.go"))
	if e != nil {
		return nil, e
	}
	_, src, ok := strings.Cut(string(b), "const defaultStorageManifest = `")
	if !ok {
		return nil, errors.New("unexpected Kind storage source")
	}
	src = strings.TrimSuffix(strings.TrimSpace(src), "`")
	for name, key := range map[string]string{"storageProvisionerImage": "localPath", "storageHelperImage": "localPathHelper"} {
		src = strings.ReplaceAll(src, "` + "+name+" + `", p.Lock.Images[key])
	}
	if strings.Contains(src, "`") {
		return nil, errors.New("unexpected Kind storage interpolation")
	}
	objs, e := p.yaml(ctx, []byte(src))
	if e != nil {
		return nil, e
	}
	var storage []Object
	for _, o := range objs {
		if o["kind"] == "Deployment" {
			mapping(mapping(mapping(o["spec"])["template"])["spec"])["nodeSelector"] = Object{"kubernetes.io/os": "linux", "node-role.local/compute": "true"}
		}
		if o["kind"] == "ConfigMap" {
			d := mapping(o["data"])
			helper := d["helperPod.yaml"].(string)
			d["helperPod.yaml"] = strings.Replace(helper, "tolerations:", "tolerations:\n    - key: node-role.local/data\n      operator: Equal\n      value: \"true\"\n      effect: NoSchedule", 1)
		}
		if o["kind"] != "Namespace" && o["kind"] != "StorageClass" {
			storage = append(storage, o)
		}
	}
	files[p.Config.Components["local-storage"]+"/rendered.yaml"] = encoded(storage)

	caps, e := p.RenderCapabilities(ctx)
	if e != nil {
		return nil, e
	}
	for path, b := range caps {
		files[path] = b
	}
	if p.currentLayout() {
		if e = p.renderCoreInputs(files); e != nil {
			return nil, e
		}
	}
	return files, nil
}
func (p *Project) Write(files map[string][]byte) error {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, path := range keys {
		full := filepath.Join(p.Root, path)
		data := files[path]
		if strings.HasPrefix(path, inputDir+"/") && strings.HasSuffix(path, ".json") {
			if resolved, err := authored.Resolve(full); err == nil && strings.HasSuffix(resolved, ".yaml") {
				var v any
				if err = json.Unmarshal(data, &v); err != nil {
					return err
				}
				data, err = authored.YAML(v)
				if err != nil {
					return err
				}
				full = resolved
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if e := os.MkdirAll(filepath.Dir(full), 0755); e != nil {
			return e
		}
		f, e := os.CreateTemp(filepath.Dir(full), ".platform-render-")
		if e != nil {
			return e
		}
		name := f.Name()
		_, e = f.Write(data)
		if e == nil {
			e = f.Chmod(0644)
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = os.Rename(name, full)
		}
		if e != nil {
			os.Remove(name)
			return e
		}
	}
	return nil
}

func reviewedDevelopmentRevision(revision string) bool {
	_, err := developmentprofile.Lookup(revision)
	return err == nil
}
