package workload

import (
	"atlas-refactor/internal/platform"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

func projectPermissions(files Files, m *Model) error {
	ns := m.Intent.Project.Name
	return edit(files, ProjectsPath, func(xs []Object) ([]Object, error) {
		for _, name := range []string{"platform-project", "workload-project"} {
			p, e := find(xs, "AppProject", "argocd", name)
			if e != nil {
				return nil, e
			}
			s := obj(p["spec"])
			s["destinations"] = append(arr(s["destinations"]), Object{"server": "https://kubernetes.default.svc", "namespace": ns})
		}
		return xs, nil
	})
}

// Certificate issuance is a prerequisite of the Gateway listeners published
// in infrastructure. Separate owners may reconcile in any order.
func certificatePrerequisites(files Files, m *Model) error {
	return edit(files, PKIPath, func(xs []Object) ([]Object, error) {
		template, e := find(xs, "Certificate", "gateway", "web-tls")
		if e != nil {
			return nil, e
		}
		for _, w := range m.Intent.Workloads {
			o := clone(template)
			name := "s2-" + ShortID("Workload", w.Project, w.Name) + "-tls"
			meta(o)["name"] = name
			obj(o["spec"])["secretName"] = name
			obj(o["spec"])["dnsNames"] = []string{w.Exposure.Hostname}
			xs = append(xs, o)
		}
		return xs, nil
	})
}

func platformAdapters(files Files, m *Model) error {
	ns := m.Intent.Project.Name
	if e := edit(files, EdgePath, func(xs []Object) ([]Object, error) {
		g, e := find(xs, "Gateway", "gateway", "development")
		if e != nil {
			return nil, e
		}
		s := obj(g["spec"])
		for _, w := range m.Intent.Workloads {
			h := ShortID("Workload", w.Project, w.Name)
			for _, secure := range []bool{false, true} {
				l := Object{"name": "s2-" + h + "-http", "hostname": w.Exposure.Hostname, "port": 80, "protocol": "HTTP", "allowedRoutes": Object{"namespaces": Object{"from": "Selector", "selector": Object{"matchLabels": Object{"kubernetes.io/metadata.name": ns, "atlas.io/project": ns}}}, "kinds": []any{Object{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute"}}}}
				if secure {
					l["name"] = "s2-" + h + "-https"
					l["port"] = 443
					l["protocol"] = "HTTPS"
					l["tls"] = Object{"mode": "Terminate", "certificateRefs": []any{Object{"group": "", "kind": "Secret", "name": "s2-" + h + "-tls"}}}
				}
				s["listeners"] = append(arr(s["listeners"]), l)
			}
		}
		return xs, nil
	}); e != nil {
		return e
	}
	if e := edit(files, SealPath, func(xs []Object) ([]Object, error) {
		extra := []Object{}
		changed := false
		for _, o := range xs {
			if meta(o)["namespace"] == "workload-web" && (o["kind"] == "Role" || o["kind"] == "RoleBinding") {
				n := clone(o)
				meta(n)["namespace"] = ns
				extra = append(extra, n)
			}
			if o["kind"] == "Role" {
				for _, r := range arr(o["rules"]) {
					rule := obj(r)
					for _, v := range arr(rule["resources"]) {
						if v == "namespaces" {
							rule["resourceNames"] = append(arr(rule["resourceNames"]), ns)
						}
					}
				}
			}
			if o["kind"] == "Deployment" && meta(o)["name"] == "sealed-secrets" {
				for _, v := range arr(val(o, "spec", "template", "spec", "containers")) {
					container := obj(v)
					args := arr(container["args"])
					for i, a := range args {
						if a == "--additional-namespaces" && i+1 < len(args) {
							args[i+1] = str(args[i+1]) + "," + ns
							changed = true
						}
					}
				}
			}
		}
		if !changed || len(extra) != 2 {
			return nil, errors.New("sealed controller namespace/RBAC template changed")
		}
		return append(xs, extra...), nil
	}); e != nil {
		return e
	}
	if e := edit(files, SealExtraPath, func(xs []Object) ([]Object, error) {
		o, e := find(xs, "ClusterRole", "", "sealed-secrets-namespace-reader")
		if e != nil {
			return nil, e
		}
		rs := arr(o["rules"])
		if len(rs) != 1 {
			return nil, errors.New("unexpected namespace reader")
		}
		obj(rs[0])["resourceNames"] = append(arr(obj(rs[0])["resourceNames"]), ns)
		return xs, nil
	}); e != nil {
		return e
	}
	metrics := false
	for _, w := range m.Intent.Workloads {
		metrics = metrics || w.Observability.Metrics
	}
	if metrics {
		if e := edit(files, MonitorPath, func(xs []Object) ([]Object, error) {
			found := false
			for _, o := range xs {
				if o["kind"] != "Prometheus" {
					continue
				}
				sel := obj(val(o, "spec", "serviceMonitorNamespaceSelector"))
				exprs := arr(sel["matchExpressions"])
				if len(exprs) != 1 || val(obj(exprs[0]), "key") != "kubernetes.io/metadata.name" || val(obj(exprs[0]), "operator") != "In" {
					return nil, errors.New("unexpected metrics namespace selector")
				}
				obj(exprs[0])["values"] = append(arr(obj(exprs[0])["values"]), ns)
				found = true
			}
			if !found {
				return nil, errors.New("monitoring capability absent")
			}
			return xs, nil
		}); e != nil {
			return e
		}
	}
	return nil
}
func validateArtifacts(c CompileContext, m *Model, a *Artifacts) error {
	if len(m.Intent.Bindings) == 0 {
		if a != nil {
			return errors.New("unexpected credential artifacts")
		}
		return nil
	}
	if a == nil || a.Schema != 1 || a.InstallID != c.InstallID || a.CertificateSHA256 != c.CertificateSHA256 || a.IntentSHA256 != Digest(JSON(m.Intent)) || len(a.Clients) != len(m.Intent.Bindings) {
		return errors.New("missing or mismatched registered credentials")
	}
	if e := sealedObject(a.Provider, "object-storage", "seaweedfs-auth", []string{"seaweedfs_s3_config"}); e != nil {
		return e
	}
	for _, b := range m.Intent.Bindings {
		if e := sealedObject(a.Clients[b.ID()], b.Project, b.Secret(), []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}); e != nil {
			return e
		}
	}
	return nil
}
func sealedObject(o Object, ns, name string, keys []string) error {
	if o["kind"] != "SealedSecret" || o["apiVersion"] != "bitnami.com/v1alpha1" || meta(o)["name"] != name || meta(o)["namespace"] != ns {
		return errors.New("sealed identity differs")
	}
	for k := range o {
		if k != "kind" && k != "apiVersion" && k != "metadata" && k != "spec" {
			return errors.New("unexpected sealed field")
		}
	}
	s := obj(o["spec"])
	for k := range s {
		if k != "encryptedData" && k != "template" {
			return errors.New("plaintext or unexpected sealed spec")
		}
	}
	for _, mt := range []Object{meta(o), obj(val(o, "spec", "template", "metadata"))} {
		if mt["namespace"] != ns || mt["name"] != name {
			return errors.New("sealed template identity differs")
		}
		for k := range mt {
			if k != "name" && k != "namespace" && k != "annotations" {
				return errors.New("unexpected sealed metadata")
			}
		}
		for k, v := range obj(mt["annotations"]) {
			if k != "argocd.argoproj.io/sync-options" || v != "Prune=confirm,Delete=false" {
				return errors.New("unexpected sealed annotation")
			}
			if strings.HasPrefix(k, "sealedsecrets.bitnami.com/") && v != "false" {
				return errors.New("sealed scope must be strict")
			}
		}
	}
	template := obj(s["template"])
	if template["type"] != "Opaque" {
		return errors.New("sealed Secret type must be Opaque")
	}
	for k := range template {
		if k != "metadata" && k != "type" {
			return errors.New("plaintext sealed template")
		}
	}
	encrypted := obj(s["encryptedData"])
	if len(encrypted) != len(keys) {
		return errors.New("sealed key inventory differs")
	}
	for _, k := range keys {
		b, e := base64.StdEncoding.DecodeString(str(encrypted[k]))
		if e != nil || len(b) < 64 {
			return errors.New("invalid ciphertext")
		}
	}
	return nil
}

// InventoryOf follows only the checked local Kustomizations of the existing
// flat Application graph. Scope and CRD schemas come from the product registry.
func InventoryOf(files Files, model *platform.ResourceModel) ([]OwnedResource, error) {
	apps := map[string]Object{}
	owners := map[string]string{"gitops/root/overlays/development": "atlas-refactor-root"}
	projects := map[string]Object{}
	for _, p := range []string{"gitops/root/overlays/development/resources.json", AppsPath, WorkloadAppsPath} {
		xs, e := objects(files[p])
		if e != nil || len(xs) == 0 {
			return nil, fmt.Errorf("missing Application catalog %s", p)
		}
		for _, o := range xs {
			n := str(meta(o)["name"])
			dir := str(val(o, "spec", "source", "path"))
			if o["kind"] != "Application" || n == "" || apps[n] != nil || dir == "" || owners[dir] != "" {
				return nil, errors.New("duplicate/invalid Application or source ownership")
			}
			if !strings.HasPrefix(dir, "gitops/") || path.Clean(dir) != dir {
				return nil, errors.New("nonlocal Application source")
			}
			apps[n] = o
			owners[dir] = n
		}
	}
	ps, e := objects(files[ProjectsPath])
	if e != nil {
		return nil, e
	}
	for _, p := range ps {
		if p["kind"] == "AppProject" {
			projects[str(meta(p)["name"])] = p
		}
	}
	seen := map[string]bool{}
	result := []OwnedResource{}
	dirs := []string{}
	for d := range owners {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		owner := owners[dir]
		var k struct {
			APIVersion string   `json:"apiVersion"`
			Kind       string   `json:"kind"`
			Resources  []string `json:"resources"`
		}
		if e := StrictDecode(files[dir+"/kustomization.yaml"], &k); e != nil || k.Kind != "Kustomization" || len(k.Resources) == 0 {
			return nil, fmt.Errorf("invalid local Kustomization %s", dir)
		}
		for _, p := range k.Resources {
			if p == "" || path.Base(p) != p || strings.ContainsAny(p, ":\\") {
				return nil, errors.New("nonlocal manifest path")
			}
			file := dir + "/" + p
			xs, e := objects(files[file])
			if e != nil || len(files[file]) == 0 {
				return nil, fmt.Errorf("missing manifest %s", file)
			}
			for _, o := range xs {
				if e = model.Validate(o, ""); e != nil {
					return nil, e
				}
				id := identity(o)
				if str(meta(o)["name"]) == "" || seen[id] {
					return nil, fmt.Errorf("duplicate/unnamed runtime identity %s", id)
				}
				seen[id] = true
				if o["kind"] == "Secret" {
					return nil, errors.New("plaintext Secret in deployment")
				}
				if o["kind"] == "NetworkPolicy" && bytes.Contains(JSON(o), []byte(`"ipBlock"`)) {
					return nil, errors.New("internal network policy uses IP authorization")
				}
				if a := apps[owner]; a != nil {
					project := str(val(a, "spec", "project"))
					if project == "atlas-bootstrap" {
						if o["kind"] != "AppProject" {
							return nil, errors.New("bootstrap leaf exceeds Project authority")
						}
					} else {
						pr := projects[project]
						if pr == nil {
							return nil, errors.New("unknown AppProject")
						}
						if e := permits(pr, o); e != nil {
							return nil, e
						}
					}
				}
				result = append(result, OwnedResource{id, owner, file, Digest(JSON(o))})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Identity < result[j].Identity })
	return result, nil
}
func permits(p, o Object) error {
	ns := str(meta(o)["namespace"])
	group, _, ok := strings.Cut(str(o["apiVersion"]), "/")
	if !ok {
		group = ""
	}
	field := "clusterResourceWhitelist"
	if ns != "" {
		field = "namespaceResourceWhitelist"
		allowed := false
		for _, d := range arr(val(p, "spec", "destinations")) {
			allowed = allowed || val(obj(d), "namespace") == ns && val(obj(d), "server") == "https://kubernetes.default.svc"
		}
		if !allowed {
			return fmt.Errorf("AppProject denies namespace %s", ns)
		}
	}
	for _, x := range arr(val(p, "spec", field)) {
		if val(obj(x), "group") == group && val(obj(x), "kind") == o["kind"] {
			return nil
		}
	}
	return fmt.Errorf("AppProject %s denies %s", meta(p)["name"], identity(o))
}

// DecodeArtifacts rejects duplicate/unknown fields and trailing values. The
// preparer canonicalizes the narrow SealedSecret envelope before registration.
func DecodeArtifacts(b []byte) (*Artifacts, error) {
	var a Artifacts
	if e := StrictDecode(b, &a); e != nil {
		return nil, e
	}
	return &a, nil
}
