package workload

import (
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Object = map[string]any
type Files = map[string][]byte

const AppsPath = "gitops/platform/applications/overlays/development/resources.json"
const WorkloadAppsPath = "gitops/workloads/applications/overlays/development/resources.json"
const ProjectsPath = "gitops/platform/management/projects/overlays/development/resources.json"
const CredentialsPath = "gitops/platform/management/platform-credentials/overlays/development/resources.json"
const EdgePath = "gitops/platform/networking/edge/overlays/development/resources.json"
const PKIPath = "gitops/platform/management/local-pki/overlays/development/resources.json"
const SealPath = "gitops/platform/management/sealed-secrets/overlays/development/rendered.yaml"
const SealExtraPath = "gitops/platform/management/sealed-secrets/overlays/development/resources.json"
const MonitorPath = "gitops/platform/observability/stack/overlays/development/rendered.yaml"
const StoragePath = "gitops/platform/storage/seaweedfs/overlays/development/resources.json"
const InventoryPath = "platform/s2-inventory.json"

type Artifacts struct {
	Schema            int               `json:"schema"`
	InstallID         string            `json:"installID"`
	CertificateSHA256 string            `json:"certificateSHA256"`
	IntentSHA256      string            `json:"intentSHA256"`
	Provider          Object            `json:"provider"`
	Clients           map[string]Object `json:"clients"`
}
type CompileContext struct {
	Base                                                                            Files
	Repository, Branch, ProductSHA256, CompilerSHA256, InstallID, CertificateSHA256 string
	HTTPSPort                                                                       int
	ResourceModel                                                                   *platform.ResourceModel
}
type OwnedResource struct {
	Identity string `json:"identity"`
	Owner    string `json:"owner"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
}
type Inventory struct {
	Schema         int               `json:"schema"`
	Phase          string            `json:"phase"`
	ProductSHA256  string            `json:"productSHA256"`
	CompilerSHA256 string            `json:"compilerSHA256"`
	BaseSHA256     string            `json:"baseSHA256"`
	IntentSHA256   string            `json:"intentSHA256"`
	Files          map[string]string `json:"files"`
	Resources      []OwnedResource   `json:"resources"`
}
type Result struct {
	Files     Files
	Inventory Inventory
}

func obj(v any) Object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func val(o Object, keys ...string) any {
	var v any = o
	for _, k := range keys {
		v = obj(v)[k]
	}
	return v
}
func str(v any) string                   { s, _ := v.(string); return s }
func clone(o Object) Object              { var v Object; _ = json.Unmarshal(JSON(o), &v); return v }
func objects(b []byte) ([]Object, error) { return platform.DecodeJSONManifests(b) }
func list(xs []Object) []byte            { return JSON(Object{"apiVersion": "v1", "kind": "List", "items": xs}) }
func meta(o Object) Object               { return obj(o["metadata"]) }
func identity(o Object) string {
	g, _, ok := strings.Cut(str(o["apiVersion"]), "/")
	if !ok {
		g = ""
	}
	return g + "/" + str(o["kind"]) + "/" + str(meta(o)["namespace"]) + "/" + str(meta(o)["name"])
}
func resource(api, kind, ns, name string, spec Object) Object {
	m := Object{"name": name, "annotations": Object{"argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}}
	if ns != "" {
		m["namespace"] = ns
	}
	o := Object{"apiVersion": api, "kind": kind, "metadata": m}
	if spec != nil {
		o["spec"] = spec
	}
	return o
}
func labels(w Workload) Object {
	return Object{"atlas.io/project": w.Project, "atlas.io/workload": w.Name}
}
func peer(ns string, pods Object) Object {
	return Object{"namespaceSelector": Object{"matchLabels": Object{"kubernetes.io/metadata.name": ns}}, "podSelector": Object{"matchLabels": pods}}
}
func policy(ns, name string, pods Object, direction string, peers []any, port int) Object {
	key, from := "ingress", "from"
	if direction == "Egress" {
		key, from = "egress", "to"
	}
	return resource("networking.k8s.io/v1", "NetworkPolicy", ns, name, Object{"podSelector": Object{"matchLabels": pods}, "policyTypes": []string{direction}, key: []any{Object{from: peers, "ports": []any{Object{"protocol": "TCP", "port": port}}}}})
}
func app(c CompileContext, name, dir, ns, project, wave string) Object {
	o := resource("argoproj.io/v1alpha1", "Application", platformnames.Argo, name, Object{"project": project, "source": Object{"repoURL": c.Repository, "targetRevision": c.Branch, "path": dir}, "destination": Object{"server": "https://kubernetes.default.svc", "namespace": ns}, "syncPolicy": Object{"automated": Object{"enabled": true, "prune": true, "selfHeal": true}, "syncOptions": []string{"ServerSideApply=true", "FailOnSharedResource=true"}}})
	obj(meta(o)["annotations"])["argocd.argoproj.io/sync-wave"] = wave
	return o
}
func addLeaf(files Files, c CompileContext, name, dir, ns, project, wave string, xs []Object) error {
	files[dir+"/resources.json"] = list(xs)
	files[dir+"/kustomization.yaml"] = JSON(Object{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": []string{"resources.json"}})
	catalog := AppsPath
	if project == "workload-project" {
		catalog = WorkloadAppsPath
	}
	as, e := objects(files[catalog])
	if e != nil {
		return e
	}
	for _, a := range as {
		if meta(a)["name"] == name {
			return errors.New("Application identity already exists")
		}
	}
	files[catalog] = list(append(as, app(c, name, dir, ns, project, wave)))
	return nil
}
func edit(files Files, p string, fn func([]Object) ([]Object, error)) error {
	xs, e := objects(files[p])
	if e != nil || len(xs) == 0 {
		return fmt.Errorf("missing/invalid adapter source %s", p)
	}
	xs, e = fn(xs)
	if e != nil {
		return e
	}
	files[p] = list(xs)
	return nil
}
func find(xs []Object, kind, ns, name string) (Object, error) {
	var found Object
	for _, o := range xs {
		if o["kind"] == kind && meta(o)["name"] == name && str(meta(o)["namespace"]) == ns {
			if found != nil {
				return nil, errors.New("duplicate adapter identity")
			}
			found = o
		}
	}
	if found == nil {
		return nil, fmt.Errorf("missing adapter object %s/%s/%s", kind, ns, name)
	}
	return found, nil
}

// Compile always starts from the immutable D1 public tree. Publishing compares
// the resulting delta with a verified predecessor; it never treats missing state
// as an empty installation. Review mode must not publish unprepared consumers.
func Compile(c CompileContext, m *Model, phase string, sealed *Artifacts) (Result, error) {
	// Check the credential-independent
	// consumer through this same lowering pipeline before any preparation or
	// publication. Its incomplete tree is private and always discarded.
	if phase == "permissions" || phase == "project" || phase == "infrastructure" {
		if _, err := compile(c, m, "consumer", nil, true); err != nil {
			return Result{}, fmt.Errorf("consumer preflight: %w", err)
		}
	}
	return compile(c, m, phase, sealed, false)
}

func compile(c CompileContext, m *Model, phase string, sealed *Artifacts, preflight bool) (Result, error) {
	if phase != "permissions" && phase != "project" && phase != "infrastructure" && phase != "consumer" {
		return Result{}, errors.New("unknown phase")
	}
	if m == nil || c.ResourceModel == nil || !shaRE.MatchString(c.ProductSHA256) || !shaRE.MatchString(c.CompilerSHA256) || c.HTTPSPort < 1024 || c.HTTPSPort > 65535 || len(c.InstallID) != 32 || !shaRE.MatchString(c.CertificateSHA256) {
		return Result{}, errors.New("incomplete compilation context")
	}
	for _, catalog := range []string{"gitops/root/overlays/development/resources.json", AppsPath, WorkloadAppsPath} {
		apps, err := objects(c.Base[catalog])
		if err != nil {
			return Result{}, err
		}
		for _, a := range apps {
			if val(a, "spec", "source", "repoURL") != c.Repository || val(a, "spec", "source", "targetRevision") != c.Branch {
				return Result{}, errors.New("Application source differs from deployment binding")
			}
		}
	}
	// AppProject destinations reserve names even before the Namespace exists.
	projects, e := objects(c.Base[ProjectsPath])
	if e != nil {
		return Result{}, e
	}
	for _, project := range projects {
		for _, destination := range arr(val(project, "spec", "destinations")) {
			if val(obj(destination), "namespace") == m.Intent.Project.Namespace() {
				return Result{}, errors.New("Project namespace is reserved by the deployment base")
			}
		}
	}
	baseInv, e := InventoryOf(c.Base, c.ResourceModel)
	if e != nil {
		return Result{}, e
	}
	for _, r := range baseInv {
		if r.Identity == "/Namespace//"+m.Intent.Project.Namespace() {
			return Result{}, errors.New("Project namespace already owned by base")
		}
	}
	files := Files{}
	for p, b := range c.Base {
		files[p] = append([]byte(nil), b...)
	}
	declarations, e := m.HumanAuthored()
	if e != nil {
		return Result{}, e
	}
	for p, b := range declarations {
		files[p] = b
	}
	// Canonical declarations must actually be installed. Arbitrary author grants
	// cannot enable capabilities or add new provider implementations.
	as, e := objects(files[AppsPath])
	if e != nil {
		return Result{}, e
	}
	cap := false
	for _, a := range as {
		cap = cap || meta(a)["name"] == "object-storage"
	}
	resolved, e := Resolve(m.Intent, cap)
	if e != nil {
		return Result{}, e
	}
	m = resolved
	p := m.Intent.Project
	if e = projectPermissions(files, m); e != nil {
		return Result{}, e
	}
	if phase != "permissions" {
		xs := projectObjects(m, phase)
		if e = addLeaf(files, c, p.AppName(), "gitops/platform/projects/"+p.Name, p.Namespace(), "platform-project", "-105", xs); e != nil {
			return Result{}, e
		}
		if e = certificatePrerequisites(files, m); e != nil {
			return Result{}, e
		}
	}
	if phase == "infrastructure" || phase == "consumer" {
		if e = platformAdapters(files, m); e != nil {
			return Result{}, e
		}
	}
	if phase == "consumer" {
		if len(m.Intent.Bindings) > 0 && !preflight {
			if e = validateArtifacts(c, m, sealed); e != nil {
				return Result{}, e
			}
		}
		for _, r := range m.Workloads {
			w := r.Definition
			if e = addLeaf(files, c, w.AppName(), "gitops/workloads/projects/"+p.Name+"/"+w.Name, p.Namespace(), "workload-project", "10", workloadObjects(c, r)); e != nil {
				return Result{}, e
			}
			if b := r.Binding; b != nil {
				if e = addLeaf(files, c, b.AppName(), "gitops/platform/bindings/"+p.Name+"/"+b.Name, p.Namespace(), "platform-project", "-5", bindingObjects(*b, w)); e != nil {
					return Result{}, e
				}
			}
		}
		if sealed != nil && len(m.Intent.Bindings) > 0 {
			if e = edit(files, CredentialsPath, func(xs []Object) ([]Object, error) {
				o, e := find(xs, "SealedSecret", platformnames.ObjectStorage, "seaweedfs-auth")
				if e != nil {
					return nil, e
				}
				for i, x := range xs {
					if identity(x) == identity(o) {
						xs[i] = clone(sealed.Provider)
					}
				}
				for _, b := range m.Intent.Bindings {
					xs = append(xs, clone(sealed.Clients[b.ID()]))
				}
				return xs, nil
			}); e != nil {
				return Result{}, e
			}
			if e = edit(files, StoragePath, func(xs []Object) ([]Object, error) {
				o, e := find(xs, "StatefulSet", platformnames.ObjectStorage, "seaweedfs")
				if e != nil {
					return nil, e
				}
				mt := obj(val(o, "spec", "template", "metadata"))
				if mt["annotations"] == nil {
					mt["annotations"] = Object{}
				}
				obj(mt["annotations"])["atlas.io/s3-auth-cipher-sha256"] = Digest(JSON(sealed.Provider))
				return xs, nil
			}); e != nil {
				return Result{}, e
			}
		}
	}
	inv, e := InventoryOf(files, c.ResourceModel)
	if e != nil {
		return Result{}, e
	}
	if e = validateDefaultStability(c, files, baseInv, inv); e != nil {
		return Result{}, e
	}
	after := map[string]OwnedResource{}
	for _, r := range inv {
		after[r.Identity] = r
	}
	for _, r := range baseInv {
		n, ok := after[r.Identity]
		if !ok || r.Owner != n.Owner {
			return Result{}, fmt.Errorf("base identity/ownership changed: %s", r.Identity)
		}
	}
	for _, p := range []string{"gitops/root/overlays/development/resources.json", "gitops/platform/networking/cilium/overlays/development/rendered.yaml", "gitops/platform/management/argocd-self/overlays/development/rendered.yaml", "gitops/platform/management/argocd-self/overlays/development/signal.json"} {
		if !bytes.Equal(files[p], c.Base[p]) {
			return Result{}, errors.New("frozen authority source changed")
		}
	}
	output := Inventory{Schema: 1, Phase: phase, ProductSHA256: c.ProductSHA256, CompilerSHA256: c.CompilerSHA256, BaseSHA256: platform.BundleDigest(c.Base), IntentSHA256: Digest(JSON(m.Intent)), Files: map[string]string{}, Resources: inv}
	for p, b := range files {
		if !bytes.Equal(b, c.Base[p]) {
			output.Files[p] = Digest(b)
		}
	}
	files[InventoryPath] = JSON(output)
	return Result{files, output}, nil
}
func projectObjects(m *Model, phase string) []Object {
	p := m.Intent.Project
	ns := resource("v1", "Namespace", "", p.Namespace(), nil)
	meta(ns)["annotations"] = Object{"argocd.argoproj.io/sync-options": "Prune=false,Delete=false", "atlas.local/owner-description": p.Owner}
	owner := p.Owner
	if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`).MatchString(owner) {
		owner = "owner-" + ShortID("owner", owner)
	}
	meta(ns)["labels"] = Object{"kubernetes.io/metadata.name": p.Namespace(), "atlas.local/class": "project", "atlas.local/domain": "application", "atlas.local/project": p.Name, "atlas.local/owner": owner, "atlas.io/project": p.Name, "atlas.io/gateway-access": "development", "pod-security.kubernetes.io/enforce": "restricted"}
	q := p.Quota
	xs := []Object{ns, resource("v1", "ResourceQuota", p.Namespace(), "project-budget", Object{"hard": Object{"pods": fmt.Sprint(q.Pods), "requests.cpu": cpu(q.RequestsCPU), "limits.cpu": cpu(q.LimitsCPU), "requests.memory": memory(q.RequestsMemory), "limits.memory": memory(q.LimitsMemory), "persistentvolumeclaims": "0"}}), resource("v1", "LimitRange", p.Namespace(), "defaults", Object{"limits": []any{Object{"type": "Container", "default": Object{"cpu": "500m", "memory": "256Mi"}, "defaultRequest": Object{"cpu": "100m", "memory": "128Mi"}}}}), resource("networking.k8s.io/v1", "NetworkPolicy", p.Namespace(), "default-deny", Object{"podSelector": Object{}, "policyTypes": []string{"Ingress", "Egress"}})}
	dns := policy(p.Namespace(), "allow-dns", Object{}, "Egress", []any{peer("kube-system", Object{"k8s-app": "kube-dns"})}, 53)
	obj(arr(val(dns, "spec", "egress"))[0])["ports"] = []any{Object{"protocol": "UDP", "port": 53}, Object{"protocol": "TCP", "port": 53}}
	xs = append(xs, dns)
	for _, r := range m.Workloads {
		w := r.Definition
		sa := resource("v1", "ServiceAccount", p.Namespace(), w.Name, nil)
		sa["automountServiceAccountToken"] = false
		xs = append(xs, sa, policy(p.Namespace(), "gateway-"+w.Name, labels(w), "Ingress", []any{peer(platformnames.Gateway, Object{"gateway.envoyproxy.io/owning-gateway-name": "development", "gateway.envoyproxy.io/owning-gateway-namespace": platformnames.Gateway})}, w.Port))
		if w.Observability.Metrics {
			xs = append(xs, policy(p.Namespace(), "metrics-"+w.Name, labels(w), "Ingress", []any{peer(platformnames.Monitoring, Object{"app.kubernetes.io/name": "prometheus"})}, w.Port))
			if phase == "consumer" {
				s := resource("monitoring.coreos.com/v1", "ServiceMonitor", p.Namespace(), w.Name, Object{"namespaceSelector": Object{"matchNames": []string{p.Namespace()}}, "selector": Object{"matchLabels": labels(w)}, "endpoints": []any{Object{"port": "http", "path": "/metrics", "interval": "30s"}}})
				meta(s)["labels"] = Object{"release": "atlas-monitoring"}
				xs = append(xs, s)
			}
		}
	}
	return xs
}
func bindingObjects(b Binding, w Workload) []Object {
	client := labels(w)
	client["atlas.io/s3-binding"] = b.ID()
	return []Object{policy(b.Namespace(), "s3-"+b.ID()+"-egress", client, "Egress", []any{peer(platformnames.ObjectStorage, Object{"app.kubernetes.io/name": "seaweedfs"})}, 8333), policy(platformnames.ObjectStorage, "s3-"+b.ID()+"-ingress", Object{"app.kubernetes.io/name": "seaweedfs"}, "Ingress", []any{peer(b.Namespace(), client)}, 8333)}
}
func workloadObjects(c CompileContext, r ResolvedWorkload) []Object {
	w := r.Definition
	ls := labels(w)
	env := []any{Object{"name": "PORT", "value": fmt.Sprint(w.Port)}}
	if b := r.Binding; b != nil {
		ls["atlas.io/s3-binding"] = b.ID()
		for _, kv := range [][2]string{{"ATLAS_S3_ENDPOINT", "http://" + platformnames.S3Address}, {"ATLAS_S3_BUCKET", b.Bucket}, {"ATLAS_S3_REGION", "us-east-1"}, {"ATLAS_S3_FORCE_PATH_STYLE", "true"}} {
			env = append(env, Object{"name": kv[0], "value": kv[1]})
		}
		for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			env = append(env, Object{"name": k, "valueFrom": Object{"secretKeyRef": Object{"name": b.Secret(), "key": k, "optional": false}}})
		}
	}
	quantity := func(q Quantity) Object {
		return Object{"cpu": cpu(q.CPU), "memory": memory(q.Memory)}
	}
	probe := func(p string) Object {
		return Object{"httpGet": Object{"path": p, "port": "http"}, "periodSeconds": 5, "timeoutSeconds": 2, "failureThreshold": 6}
	}
	container := Object{"name": "web", "image": w.Image, "imagePullPolicy": "IfNotPresent", "ports": []any{Object{"name": "http", "containerPort": w.Port}}, "env": env, "resources": Object{"requests": quantity(w.Resources.Requests), "limits": quantity(w.Resources.Limits)}, "securityContext": Object{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": Object{"drop": []string{"ALL"}}}, "readinessProbe": probe("/readyz"), "livenessProbe": probe("/healthz"), "volumeMounts": []any{Object{"name": "tmp", "mountPath": "/tmp"}}}
	deploy := resource("apps/v1", "Deployment", w.Namespace(), w.Name, Object{"replicas": w.Replicas, "strategy": Object{"type": "RollingUpdate", "rollingUpdate": Object{"maxSurge": 1, "maxUnavailable": 0}}, "selector": Object{"matchLabels": labels(w)}, "template": Object{"metadata": Object{"labels": ls}, "spec": Object{"serviceAccountName": w.Name, "automountServiceAccountToken": false, "nodeSelector": Object{"node-role.local/compute": "true", "kubernetes.io/os": "linux"}, "securityContext": Object{"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532, "seccompProfile": Object{"type": "RuntimeDefault"}}, "containers": []any{container}, "volumes": []any{Object{"name": "tmp", "emptyDir": Object{"medium": "Memory", "sizeLimit": "32Mi"}}}}}})
	service := resource("v1", "Service", w.Namespace(), w.Name, Object{"selector": labels(w), "ports": []any{Object{"name": "http", "port": w.Port, "targetPort": "http"}}})
	meta(service)["labels"] = labels(w)
	routes := []Object{}
	for _, https := range []bool{false, true} {
		prefix := w.Name + "-http"
		listener := "s2-" + ShortID("Workload", w.Project, w.Name) + "-http"
		// Emit the locked Gateway API defaults so SSA does not leave routes
		// perpetually OutOfSync after the API materializes omitted fields.
		rule := Object{"matches": []any{Object{"path": Object{"type": "PathPrefix", "value": "/"}}}, "filters": []any{Object{"type": "RequestRedirect", "requestRedirect": Object{"scheme": "https", "port": c.HTTPSPort, "statusCode": 301}}}}
		if https {
			prefix = w.Name + "-https"
			listener += "s"
			rule = Object{"backendRefs": []any{Object{"group": "", "kind": "Service", "name": w.Name, "port": w.Port, "weight": 1}}, "matches": []any{Object{"path": Object{"type": "PathPrefix", "value": "/"}}}}
		}
		routes = append(routes, resource("gateway.networking.k8s.io/v1", "HTTPRoute", w.Namespace(), prefix, Object{"parentRefs": []any{Object{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "development", "namespace": platformnames.Gateway, "sectionName": listener}}, "hostnames": []string{w.Exposure.Hostname}, "rules": []any{rule}}))
	}
	return append([]Object{deploy, service}, routes...)
}

// Emit canonical Kubernetes quantities so API serialization cannot turn an
// unchanged authored integer budget into apparent live drift.
func cpu(milli int64) string {
	if milli%1000 == 0 {
		return fmt.Sprint(milli / 1000)
	}
	return fmt.Sprintf("%dm", milli)
}
func memory(mib int64) string {
	if mib%(1024*1024) == 0 {
		return fmt.Sprintf("%dTi", mib/(1024*1024))
	}
	if mib%1024 == 0 {
		return fmt.Sprintf("%dGi", mib/1024)
	}
	return fmt.Sprintf("%dMi", mib)
}
