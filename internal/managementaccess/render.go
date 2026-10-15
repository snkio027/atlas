// Package managementaccess prepares a fixed, opt-in Tier-1 access change.
// It has no Kubernetes, Git publication, credential or recovery authority.
package managementaccess

import (
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"atlas-refactor/internal/workload"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
)

type Object = map[string]any
type Files = map[string][]byte

type Endpoint struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Service     string            `json:"service"`
	ServicePort int               `json:"servicePort"`
	PodPort     int               `json:"podPort"`
	Selector    map[string]string `json:"selector"`
	ReadOnly    bool              `json:"readOnly"`
}

// Explicit allowlist, not automatic Service exposure. Internal control,
// storage topology, reloader and metrics-exporter ports stay private.
func Endpoints() []Endpoint {
	return []Endpoint{
		{"grafana", platformnames.Monitoring, "atlas-monitoring-grafana", 80, 3000, map[string]string{"app.kubernetes.io/name": "grafana", "app.kubernetes.io/instance": "atlas-monitoring"}, false},
		{"argocd", platformnames.Argo, "atlas-refactor-argocd-server", 80, 8080, map[string]string{"app.kubernetes.io/name": "argocd-server", "app.kubernetes.io/instance": "atlas-refactor-argocd"}, false},
		{"prometheus", platformnames.Monitoring, "atlas-monitoring-prometheus", 9090, 9090, map[string]string{"app.kubernetes.io/name": "prometheus", "operator.prometheus.io/name": "atlas-monitoring-prometheus"}, true},
		{"alertmanager", platformnames.Monitoring, "atlas-monitoring-alertmanager", 9093, 9093, map[string]string{"app.kubernetes.io/name": "alertmanager", "alertmanager": "atlas-monitoring-alertmanager"}, true},
		{"s3", platformnames.ObjectStorage, "seaweedfs", 8333, 8333, map[string]string{"app.kubernetes.io/name": "seaweedfs"}, false},
	}
}

type Result struct {
	Prerequisites Files // Changed files only; publish and verify before Routes.
	Routes        Files
}

func obj(v any) Object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func at(o Object, keys ...string) any {
	var v any = o
	for _, k := range keys {
		v = obj(v)[k]
	}
	return v
}
func id(o Object) string {
	return fmt.Sprint(o["apiVersion"], "/", o["kind"], "/", at(o, "metadata", "namespace"), "/", at(o, "metadata", "name"))
}
func clone(o Object) Object {
	var out Object
	_ = json.Unmarshal(workload.JSON(o), &out)
	return out
}
func list(xs []Object) []byte {
	return workload.JSON(Object{"apiVersion": "v1", "kind": "List", "items": xs})
}
func resource(api, kind, ns, name string, spec Object) Object {
	return Object{"apiVersion": api, "kind": kind, "metadata": Object{"name": name, "namespace": ns, "annotations": Object{"argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}}, "spec": spec}
}
func get(xs []Object, kind, ns, name string) (Object, error) {
	var found Object
	for _, o := range xs {
		if o["kind"] == kind && at(o, "metadata", "namespace") == ns && at(o, "metadata", "name") == name {
			if found != nil {
				return nil, errors.New("duplicate access prerequisite")
			}
			found = o
		}
	}
	if found == nil {
		return nil, fmt.Errorf("missing access prerequisite %s/%s/%s", kind, ns, name)
	}
	return found, nil
}

// Render preserves all existing identities/owners and bytes outside four
// reviewed files. Both phases are validated before producing any output. It
// does not activate an incomplete monitoring/storage installation.
func Render(base Files, model *platform.ResourceModel, httpsPort int) (Result, error) {
	if httpsPort < 1024 || httpsPort > 65535 {
		return Result{}, errors.New("expected unprivileged loopback HTTPS port")
	}
	if model == nil {
		return Result{}, errors.New("resource model required")
	}
	inv, err := workload.InventoryOf(base, model)
	if err != nil {
		return Result{}, err
	}

	expectedOwners := map[string]string{workload.PKIPath: "local-pki", workload.EdgePath: "edge", workload.MonitorPath: "monitoring", workload.ProjectsPath: "project-bootstrap"}
	for _, r := range inv {
		if want, ok := expectedOwners[r.Path]; ok && r.Owner != want {
			return Result{}, fmt.Errorf("unexpected owner for %s", r.Path)
		}
	}
	objects := []Object{}
	for _, path := range []string{workload.PKIPath, workload.ProjectsPath, workload.EdgePath, workload.MonitorPath, workload.StoragePath, "gitops/platform/management/argocd-self/overlays/development/rendered.yaml"} {
		xs, e := platform.DecodeJSONManifests(base[path])
		if e != nil || len(xs) == 0 {
			return Result{}, fmt.Errorf("missing/invalid access source %s", path)
		}
		objects = append(objects, xs...)
	}
	for _, e := range Endpoints() {
		svc, err := get(objects, "Service", e.Namespace, e.Service)
		if err != nil {
			return Result{}, err
		}
		for k, v := range e.Selector {
			if at(svc, "spec", "selector", k) != v {
				return Result{}, fmt.Errorf("%s Service selector differs", e.Name)
			}
		}
		if len(obj(at(svc, "spec", "selector"))) != len(e.Selector) {
			return Result{}, fmt.Errorf("%s Service selector differs", e.Name)
		}
		if err := backendPort(objects, svc, e); err != nil {
			return Result{}, err
		}
	}
	cm, err := get(objects, "ConfigMap", platformnames.Argo, "argocd-cmd-params-cm")
	if err != nil {
		return Result{}, err
	}
	if at(cm, "data", "server.insecure") != "true" {
		return Result{}, errors.New("Argo HTTP backend is not enabled")
	}
	pki, _ := platform.DecodeJSONManifests(base[workload.PKIPath])
	edge, _ := platform.DecodeJSONManifests(base[workload.EdgePath])
	gateway, err := get(edge, "Gateway", platformnames.Gateway, "development")
	if err != nil {
		return Result{}, err
	}
	// The installed edge contract reconciles Gateway in wave 2. Routes must
	// wait until its new listeners exist; wave 0 routes block that update.
	if at(gateway, "metadata", "annotations", "argocd.argoproj.io/sync-wave") != "2" {
		return Result{}, errors.New("unsupported Gateway sync wave; expected 2")
	}
	issuer, err := get(pki, "Issuer", platformnames.Gateway, "development-ca")
	if err != nil {
		return Result{}, err
	}
	if at(issuer, "spec", "ca", "secretName") != "development-ca" {
		return Result{}, errors.New("development CA issuer differs")
	}
	listeners := arr(at(gateway, "spec", "listeners"))
	for _, e := range Endpoints() {
		name := "management-" + e.Name
		host := e.Name + ".atlas.test"
		for _, l := range listeners {
			if at(obj(l), "name") == name+"-http" || at(obj(l), "name") == name+"-https" || at(obj(l), "hostname") == host {
				return Result{}, fmt.Errorf("management listener already exists: %s", e.Name)
			}
		}
		pki = append(pki, resource("cert-manager.io/v1", "Certificate", platformnames.Gateway, name+"-tls", Object{
			"secretName": name + "-tls", "dnsNames": []string{host}, "duration": "720h", "renewBefore": "168h",
			"privateKey": Object{"algorithm": "ECDSA", "size": 256, "rotationPolicy": "Always"},
			"issuerRef":  Object{"name": "development-ca", "kind": "Issuer", "group": "cert-manager.io"},
		}))
		for _, protocol := range []string{"http", "https"} {
			l := Object{"name": name + "-" + protocol, "hostname": host, "port": 80, "protocol": "HTTP",
				"allowedRoutes": Object{"namespaces": Object{"from": "Selector", "selector": Object{"matchLabels": Object{"kubernetes.io/metadata.name": e.Namespace}}}, "kinds": []any{Object{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute"}}}}
			if protocol == "https" {
				l["port"] = 443
				l["protocol"] = "HTTPS"
				l["tls"] = Object{"mode": "Terminate", "certificateRefs": []any{Object{"group": "", "kind": "Secret", "name": name + "-tls"}}}
			}
			listeners = append(listeners, l)
			matches := []any{Object{"path": Object{"type": "PathPrefix", "value": "/"}}}
			if e.ReadOnly {
				matches = []any{Object{"path": Object{"type": "PathPrefix", "value": "/"}, "method": "GET"}, Object{"path": Object{"type": "PathPrefix", "value": "/"}, "method": "HEAD"}}
			}
			rule := Object{"matches": matches}
			if protocol == "http" {
				rule["filters"] = []any{Object{"type": "RequestRedirect", "requestRedirect": Object{"scheme": "https", "port": httpsPort, "statusCode": 308}}}
			} else {
				rule["backendRefs"] = []any{Object{"group": "", "kind": "Service", "name": e.Service, "port": e.ServicePort, "weight": 1}}
			}
			route := resource("gateway.networking.k8s.io/v1", "HTTPRoute", e.Namespace, name+"-"+protocol, Object{
				"hostnames": []string{host}, "parentRefs": []any{Object{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "development", "namespace": platformnames.Gateway, "sectionName": name + "-" + protocol}}, "rules": []any{rule},
			})
			obj(at(route, "metadata", "annotations"))["argocd.argoproj.io/sync-wave"] = "3"
			edge = append(edge, route)
		}
		edge = append(edge, resource("networking.k8s.io/v1", "NetworkPolicy", e.Namespace, name+"-ingress", Object{
			"podSelector": Object{"matchLabels": e.Selector}, "policyTypes": []string{"Ingress"},
			"ingress": []any{Object{"from": []any{Object{
				"namespaceSelector": Object{"matchLabels": Object{"kubernetes.io/metadata.name": platformnames.Gateway}},
				"podSelector":       Object{"matchLabels": Object{"gateway.envoyproxy.io/owning-gateway-name": "development", "gateway.envoyproxy.io/owning-gateway-namespace": platformnames.Gateway}},
			}}, "ports": []any{Object{"protocol": "TCP", "port": e.PodPort}}}},
		}))
	}
	obj(gateway["spec"])["listeners"] = listeners
	monitoring, err := monitorURLs(base[workload.MonitorPath], httpsPort)
	if err != nil {
		return Result{}, err
	}
	projects, err := platform.DecodeJSONManifests(base[workload.ProjectsPath])
	if err != nil {
		return Result{}, err
	}
	project, err := get(projects, "AppProject", platformnames.Argo, "platform-project")
	if err != nil {
		return Result{}, err
	}
	rules := arr(at(project, "spec", "namespaceResourceWhitelist"))
	allowed := false
	for _, r := range rules {
		allowed = allowed || at(obj(r), "group") == "gateway.networking.k8s.io" && at(obj(r), "kind") == "HTTPRoute"
	}
	prerequisites := Files{workload.PKIPath: list(pki)}
	if !allowed {
		obj(project["spec"])["namespaceResourceWhitelist"] = append(rules, Object{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute"})
		prerequisites[workload.ProjectsPath] = list(projects)
	}
	result := Result{Prerequisites: prerequisites, Routes: Files{workload.EdgePath: list(edge), workload.MonitorPath: monitoring}}
	// Validate defaults against the deployed CRDs, not an unrelated new schema.
	before := map[string]Object{}
	for _, o := range objects {
		before[id(o)] = o
	}
	for _, phase := range []Files{result.Prerequisites, result.Routes} {
		for _, path := range sortedPaths(phase) {
			b := phase[path]
			xs, err := platform.DecodeJSONManifests(b)
			if err != nil {
				return Result{}, err
			}
			for _, o := range xs {
				if reflect.DeepEqual(before[id(o)], o) {
					continue
				}
				typ, err := model.Resolve(o)
				if err != nil {
					return Result{}, err
				}
				if typ.Schema != nil {
					if err = checkDeployedSchema(base, model, o); err != nil {
						return Result{}, err
					}
				}
				if err = model.ValidateCRDDefaultStability(before[id(o)], o); err != nil {
					return Result{}, err
				}
			}
		}
	}
	cumulative := Files{}
	for k, v := range base {
		cumulative[k] = v
	}
	for _, phase := range []Files{result.Prerequisites, result.Routes} {
		for k, v := range phase {
			cumulative[k] = v
		}
		next, err := workload.InventoryOf(cumulative, model)
		if err != nil {
			return Result{}, err
		}
		for _, old := range inv {
			i := slices.IndexFunc(next, func(n workload.OwnedResource) bool { return n.Identity == old.Identity })
			if i < 0 || next[i].Owner != old.Owner || next[i].Path != old.Path {
				return Result{}, errors.New("existing ownership changed")
			}
		}
	}
	return result, nil
}

func monitorURLs(raw []byte, port int) ([]byte, error) {
	xs, err := platform.DecodeJSONManifests(raw)
	if err != nil {
		return nil, err
	}
	d, err := get(xs, "Deployment", platformnames.Monitoring, "atlas-monitoring-grafana")
	if err != nil {
		return nil, err
	}
	found := false
	for _, v := range arr(at(d, "spec", "template", "spec", "containers")) {
		c := obj(v)
		if c["name"] != "grafana" {
			continue
		}
		found = true
		env := arr(c["env"])
		for _, v := range env {
			if at(obj(v), "name") == "GF_SERVER_ROOT_URL" || at(obj(v), "name") == "GF_SECURITY_COOKIE_SECURE" {
				return nil, errors.New("Grafana external URL already configured")
			}
		}
		c["env"] = append(env, Object{"name": "GF_SERVER_ROOT_URL", "value": fmt.Sprintf("https://grafana.atlas.test:%d/", port)}, Object{"name": "GF_SECURITY_COOKIE_SECURE", "value": "true"})
	}
	if !found {
		return nil, errors.New("Grafana container missing")
	}
	for _, v := range []struct{ kind, name, host string }{{"Prometheus", "atlas-monitoring-prometheus", "prometheus"}, {"Alertmanager", "atlas-monitoring-alertmanager", "alertmanager"}} {
		o, err := get(xs, v.kind, platformnames.Monitoring, v.name)
		if err != nil {
			return nil, err
		}
		obj(o["spec"])["externalUrl"] = fmt.Sprintf("https://%s.atlas.test:%d", v.host, port)
	}
	// The installed compiler stores a List; source renders may use a JSON YAML
	// stream. Only the three intended objects change semantically in either form.
	var original Object
	if err := json.Unmarshal(raw, &original); err == nil && original["kind"] == "List" {
		return list(xs), nil
	}
	parts := bytes.Split(raw, []byte("\n---\n"))
	for i, b := range parts {
		docs, err := platform.DecodeJSONManifests(b)
		if err != nil || len(docs) != 1 {
			return nil, errors.New("invalid monitoring document")
		}
		for _, o := range xs {
			if id(o) == id(docs[0]) && !reflect.DeepEqual(o, docs[0]) {
				parts[i] = bytes.TrimSuffix(workload.JSON(o), []byte("\n"))
			}
		}
	}
	return bytes.Join(parts, []byte("\n---\n")), nil
}

func checkDeployedSchema(base Files, model *platform.ResourceModel, o Object) error {
	typ, err := model.Resolve(o)
	if err != nil {
		return err
	}
	for _, path := range sortedPaths(base) {
		b := base[path]
		if len(path) < 7 || path[:7] != "gitops/" {
			continue
		}
		if !bytes.Contains(b, []byte(`"CustomResourceDefinition"`)) {
			continue
		}
		docs, err := platform.DecodeJSONManifests(b)
		if err != nil {
			return err
		}
		for _, d := range docs {
			if d["kind"] != "CustomResourceDefinition" || at(d, "spec", "names", "kind") != o["kind"] {
				continue
			}
			for _, v := range arr(at(d, "spec", "versions")) {
				if fmt.Sprint(at(d, "spec", "group"))+"/"+fmt.Sprint(obj(v)["name"]) == o["apiVersion"] && obj(v)["served"] == true && at(d, "spec", "scope") == string(typ.Scope) && reflect.DeepEqual(at(obj(v), "schema", "openAPIV3Schema"), typ.Schema) {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("deployed CRD schema differs for %s", o["kind"])
}

// NetworkPolicy uses the destination Pod port, not the Service port. Resolve
// named ports against the declared Deployment; Operator web ports are numeric.
func backendPort(objects []Object, svc Object, endpoint Endpoint) error {
	for _, raw := range arr(at(svc, "spec", "ports")) {
		port := obj(raw)
		if port["port"] != float64(endpoint.ServicePort) {
			continue
		}
		if port["protocol"] != nil && port["protocol"] != "TCP" {
			break
		}
		if n, ok := port["targetPort"].(float64); ok && n == float64(endpoint.PodPort) {
			return nil
		}
		name, ok := port["targetPort"].(string)
		if !ok {
			break
		}
		kind := "Deployment"
		if endpoint.Name == "s3" {
			kind = "StatefulSet"
		}
		deployment, err := get(objects, kind, endpoint.Namespace, endpoint.Service)
		if err != nil {
			return err
		}
		for k, v := range endpoint.Selector {
			if at(deployment, "spec", "template", "metadata", "labels", k) != v {
				return fmt.Errorf("%s backend Pod selector differs", endpoint.Name)
			}
		}
		for _, c := range arr(at(deployment, "spec", "template", "spec", "containers")) {
			for _, p := range arr(at(obj(c), "ports")) {
				if at(obj(p), "name") == name && at(obj(p), "containerPort") == float64(endpoint.PodPort) && (at(obj(p), "protocol") == nil || at(obj(p), "protocol") == "TCP") {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("%s Service/Pod port differs", endpoint.Name)
}

func sortedPaths(files Files) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
