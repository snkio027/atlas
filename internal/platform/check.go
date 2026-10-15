package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var pinnedImage = regexp.MustCompile(`^[a-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9_./-]+:[A-Za-z0-9_.-]+@sha256:[a-f0-9]{64}$`)

func mapping(v any) Object     { m, _ := v.(map[string]any); return m }
func slice(v any) []any        { s, _ := v.([]any); return s }
func str(v any) string         { s, _ := v.(string); return s }
func metadata(o Object) Object { return mapping(o["metadata"]) }
func group(o Object) string {
	g, _, ok := strings.Cut(str(o["apiVersion"]), "/")
	if !ok {
		return ""
	}
	return g
}
func identity(o Object) string {
	return group(o) + "/" + str(o["kind"]) + "/" + str(metadata(o)["namespace"]) + "/" + str(metadata(o)["name"])
}
func field(o Object, keys ...string) any {
	var v any = o
	for _, k := range keys {
		v = mapping(v)[k]
	}
	return v
}
func contains(v any, want string) bool {
	for _, x := range slice(v) {
		if x == want {
			return true
		}
	}
	return false
}
func allowed(v any, g, k string) bool {
	for _, r := range slice(v) {
		m := mapping(r)
		if m["group"] == g && m["kind"] == k {
			return true
		}
	}
	return false
}
func (p *Project) imageLocked(s string) bool {
	for _, im := range p.Lock.Images {
		if im == s {
			return true
		}
	}
	return false
}
func (p *Project) images(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "image" || k == "imageName" {
				if !p.imageLocked(str(v)) {
					return fmt.Errorf("unlocked image %v", v)
				}
			} else if e := p.images(v); e != nil {
				return e
			}
		}
	case []any:
		for _, v := range x {
			if e := p.images(v); e != nil {
				return e
			}
		}
	case string:
		// Controller command arguments and embedded helper specs also carry images.
		for _, word := range strings.FieldsFunc(x, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '=' || r == '"' }) {
			if strings.Contains(word, "@sha256:") && !p.imageLocked(word) {
				return fmt.Errorf("unlocked embedded image %s", word)
			}
		}
	}
	return nil
}
func checkObject(o Object) error {
	if str(metadata(o)["name"]) == "" {
		return errors.New("unnamed resource")
	}
	switch o["kind"] {
	case "Secret":
		return errors.New("Secret objects must be generated at runtime, not committed")
	case "Ingress", "Endpoints", "EndpointSlice", "CiliumNetworkPolicy", "CiliumClusterwideNetworkPolicy":
		return fmt.Errorf("prohibited application/network resource: %s", o["kind"])
	case "NetworkPolicy":
		b, _ := json.Marshal(o)
		if bytes.Contains(b, []byte(`"ipBlock"`)) {
			return errors.New("internal network authorization must use workload identity selectors")
		}
	case "Application":
		for _, f := range slice(metadata(o)["finalizers"]) {
			if strings.Contains(str(f), "resources-finalizer.argocd.argoproj.io") {
				return errors.New("cascading Application finalizer")
			}
		}
		if field(o, "spec", "syncPolicy", "automated", "enabled") != true || field(o, "spec", "syncPolicy", "automated", "selfHeal") != true {
			return errors.New("child Application must auto-sync and self-heal")
		}
		if !contains(field(o, "spec", "syncPolicy", "syncOptions"), "ServerSideApply=true") || contains(field(o, "spec", "syncPolicy", "syncOptions"), "ApplyOutOfSyncOnly=true") {
			return errors.New("full SSA synchronization is required for initial adoption")
		}
		if metadata(o)["name"] != "atlas-refactor-root" && !strings.Contains(str(field(o, "metadata", "annotations", "argocd.argoproj.io/sync-options")), "Prune=confirm") {
			return errors.New("child Application missing prune protection")
		}
	case "AppProject":
		b, _ := json.Marshal(o["spec"])
		if bytes.Contains(b, []byte(`"*"`)) {
			return errors.New("AppProject wildcard capability")
		}
	case "PersistentVolumeClaim":
		if field(o, "spec", "storageClassName") != "atlas-local-retain" {
			return errors.New("workload must request the retained local storage class")
		}
		if !strings.Contains(str(field(o, "metadata", "annotations", "argocd.argoproj.io/sync-options")), "Delete=false") {
			return errors.New("PVC requires deletion protection")
		}
	}
	return nil
}

// validateSchema checks the structural subset locally. CEL, admission behavior,
// Kubernetes built-ins, and controller reconciliation still require a real API.
func validateSchema(value any, s Object, path string) error {
	if value == nil {
		if s["nullable"] == true {
			return nil
		}
		return fmt.Errorf("%s: null is not allowed", path)
	}
	if s["x-kubernetes-preserve-unknown-fields"] == true {
		return nil
	}
	typ := str(s["type"])
	valid := true
	switch typ {
	case "object":
		_, valid = value.(map[string]any)
	case "array":
		_, valid = value.([]any)
	case "string":
		_, valid = value.(string)
	case "boolean":
		_, valid = value.(bool)
	case "number", "integer":
		n, ok := value.(float64)
		valid = ok
		if typ == "integer" && ok {
			valid = n == float64(int64(n))
		}
	}
	if s["x-kubernetes-int-or-string"] == true {
		_, a := value.(string)
		_, b := value.(float64)
		valid = a || b
	}
	if !valid {
		return fmt.Errorf("%s: expected %s", path, typ)
	}
	if choices := slice(s["enum"]); len(choices) > 0 {
		found := false
		for _, choice := range choices {
			if fmt.Sprint(choice) == fmt.Sprint(value) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s: value %v not in enum", path, value)
		}
	}
	if m, ok := value.(map[string]any); ok {
		props := mapping(s["properties"])
		for _, required := range slice(s["required"]) {
			if _, ok := m[str(required)]; !ok {
				return fmt.Errorf("%s: missing %s", path, required)
			}
		}
		for key, v := range m {
			if path == "$" && (key == "apiVersion" || key == "kind" || key == "metadata") {
				continue
			}
			child, exists := props[key]
			if !exists {
				if s["additionalProperties"] == true {
					continue
				}
				child, exists = s["additionalProperties"]
				if !exists {
					return fmt.Errorf("%s: unknown field %s", path, key)
				}
			}
			if e := validateSchema(v, mapping(child), path+"."+key); e != nil {
				return e
			}
		}
	}
	if a, ok := value.([]any); ok {
		for i, v := range a {
			if e := validateSchema(v, mapping(s["items"]), fmt.Sprintf("%s[%d]", path, i)); e != nil {
				return e
			}
		}
	}
	return nil
}
func (p *Project) Check(ctx context.Context) (int, error) {
	if e := p.ValidateCapabilityProjection(); e != nil {
		return 0, e
	}
	if _, e := p.CheckCapabilities(ctx); e != nil {
		return 0, e
	}
	files, e := p.Render(ctx)
	if e != nil {
		return 0, e
	}
	for path, want := range files {
		got, e := readInput(filepath.Join(p.Root, path))
		if e != nil {
			return 0, e
		}
		if !bytes.Equal(got, want) {
			return 0, fmt.Errorf("stale generated artifact: %s (run platform:render)", path)
		}
	}
	dirs := []string{p.Config.RootPath, "gitops/platform/applications/overlays/development", "gitops/platform/management/projects/overlays/development", "gitops/workloads/applications/overlays/development", "gitops/workloads/web-smoke/overlays/development"}
	for _, dir := range p.Config.Components {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	bundles := map[string][]Object{}
	inventory := map[string]Object{}
	for _, dir := range dirs {
		var k Object
		if e := readJSON(filepath.Join(p.Root, dir, "kustomization.yaml"), &k); e != nil {
			return 0, e
		}
		if len(k) != 3 {
			return 0, fmt.Errorf("only local resources are allowed in %s", dir)
		}
		for _, res := range slice(k["resources"]) {
			path := str(res)
			if path == "" || filepath.Base(path) != path || strings.Contains(path, ":") {
				return 0, fmt.Errorf("non-local Kustomize input: %s", path)
			}
		}
		b, e := p.run(ctx, nil, p.Tools.Kubectl, "kustomize", dir)
		if e != nil {
			return 0, e
		}
		objs, e := p.yaml(ctx, b)
		if e != nil {
			return 0, e
		}
		bundles[dir] = objs
		for _, o := range objs {
			id := identity(o)
			if _, ok := inventory[id]; ok {
				return 0, fmt.Errorf("duplicate GitOps owner for %s", id)
			}
			inventory[id] = o
			if e := checkObject(o); e != nil {
				return 0, fmt.Errorf("%s: %w", id, e)
			}
			if o["kind"] != "CustomResourceDefinition" {
				if e := p.images(o); e != nil {
					return 0, fmt.Errorf("%s: %w", id, e)
				}
			}
		}
	}
	all := []Object{}
	for _, o := range inventory {
		all = append(all, o)
	}
	model, e := p.resourceModel(all)
	if e != nil {
		return 0, e
	}
	projects := map[string]Object{}
	for _, o := range inventory {
		if o["kind"] == "AppProject" {
			projects[str(metadata(o)["name"])] = o
		}
	}
	var root, bootstrapProject Object
	if e := readJSON(filepath.Join(p.Root, inputDir, "bootstrap/root.json"), &root); e != nil {
		return 0, e
	}
	if e := readJSON(filepath.Join(p.Root, inputDir, "bootstrap/project.json"), &bootstrapProject); e != nil {
		return 0, e
	}
	projects["atlas-bootstrap"] = bootstrapProject
	if _, ok := inventory[identity(root)]; ok {
		return 0, errors.New("External Root is self-managed")
	}
	if root["kind"] != "Application" || metadata(root)["name"] != "atlas-refactor-root" || field(root, "spec", "project") != "atlas-bootstrap" || field(root, "spec", "source", "path") != p.Config.RootPath {
		return 0, errors.New("invalid External Root")
	}
	if e := checkObject(root); e != nil {
		return 0, e
	}
	for _, project := range projects {
		if e := checkObject(project); e != nil {
			return 0, e
		}
		if !contains(field(project, "spec", "sourceRepos"), p.Config.RepositoryURL) {
			return 0, errors.New("project source not permitted")
		}
	}
	tenant := projects["workload-project"]
	if len(slice(field(tenant, "spec", "clusterResourceWhitelist"))) != 0 {
		return 0, errors.New("tenant cluster authority")
	}
	dests := slice(field(tenant, "spec", "destinations"))
	if len(dests) != 1 || mapping(dests[0])["namespace"] != p.namespace("workload-web") {
		return 0, errors.New("tenant namespace escape")
	}
	for _, kind := range []string{"Application", "AppProject", "ApplicationSet"} {
		if allowed(field(tenant, "spec", "namespaceResourceWhitelist"), "argoproj.io", kind) {
			return 0, errors.New("tenant Argo authority")
		}
	}
	rootChildren := bundles[p.Config.RootPath]
	want := map[string]string{"project-bootstrap": "-110", "platform-control": "-100", "workload-control": "0"}
	if len(rootChildren) != len(want) {
		return 0, errors.New("Root must contain only the three macro Applications")
	}
	for _, o := range rootChildren {
		n := str(metadata(o)["name"])
		wave, ok := want[n]
		if !ok || o["kind"] != "Application" || field(o, "metadata", "annotations", "argocd.argoproj.io/sync-wave") != wave {
			return 0, errors.New("invalid Root macro DAG")
		}
		if n != "project-bootstrap" && field(o, "spec", "project") != "platform-project" {
			return 0, errors.New("control orchestrators must use platform-project")
		}
	}
	for _, app := range append([]Object{root}, allApplications(inventory)...) {
		source := mapping(field(app, "spec", "source"))
		if source["repoURL"] != p.Config.RepositoryURL || source["targetRevision"] != p.Config.Revision {
			return 0, errors.New("Application source/revision drift")
		}
		dir := str(source["path"])
		resources, ok := bundles[dir]
		if !ok {
			return 0, fmt.Errorf("unknown Application source path %s", dir)
		}
		project, ok := projects[str(field(app, "spec", "project"))]
		if !ok {
			return 0, errors.New("unknown AppProject")
		}
		for _, o := range resources {
			if e := permitted(project, app, o, model); e != nil {
				return 0, e
			}
		}
		// Only the Root and the two controls may contain child Applications.
		n := str(metadata(app)["name"])
		if n != "atlas-refactor-root" && n != "platform-control" && n != "workload-control" {
			for _, o := range resources {
				if o["kind"] == "Application" {
					return 0, errors.New("Application nesting exceeds Root -> Control -> Leaf")
				}
			}
		}
	}
	if e := checkRoutes(inventory, p.gatewayNamespace()); e != nil {
		return 0, e
	}
	if e := p.checkNetwork(inventory); e != nil {
		return 0, e
	}
	// Parse controller configuration rather than overlooking images in YAML strings.
	cm := inventory["/ConfigMap/"+p.namespace("envoy-gateway-system")+"/envoy-gateway-config"]
	embedded, e := p.yaml(ctx, []byte(str(field(cm, "data", "envoy-gateway.yaml"))))
	if e != nil {
		return 0, e
	}
	for _, o := range embedded {
		if e := p.images(o); e != nil {
			return 0, e
		}
	}
	return len(inventory), nil
}
func allApplications(inventory map[string]Object) []Object {
	var out []Object
	for _, o := range inventory {
		if o["kind"] == "Application" {
			out = append(out, o)
		}
	}
	return out
}
func permitted(project, app, o Object, model *ResourceModel) error {
	if e := model.Validate(o, str(field(app, "spec", "destination", "namespace"))); e != nil {
		return e
	}
	resource, e := model.Resolve(o)
	if e != nil {
		return e
	}
	cluster := resource.Scope == ClusterScope
	s := mapping(project["spec"])
	g, k := group(o), str(o["kind"])
	if cluster {
		if !allowed(s["clusterResourceWhitelist"], g, k) {
			return fmt.Errorf("project %s denies cluster %s", metadata(project)["name"], identity(o))
		}
		return nil
	}
	ns := str(metadata(o)["namespace"])
	if ns == "" {
		ns = str(field(app, "spec", "destination", "namespace"))
	}
	found := false
	for _, d := range slice(s["destinations"]) {
		m := mapping(d)
		if m["namespace"] == ns && m["server"] == field(app, "spec", "destination", "server") {
			found = true
		}
	}
	if !found || !allowed(s["namespaceResourceWhitelist"], g, k) {
		return fmt.Errorf("project %s denies %s in %s", metadata(project)["name"], identity(o), ns)
	}
	return nil
}
func (p *Project) checkNetwork(inv map[string]Object) error {
	cm := inv["/ConfigMap/kube-system/cilium-config"]
	if field(cm, "data", "enable-ipv4") != "true" || field(cm, "data", "enable-ipv6") != "false" || field(cm, "data", "enable-l7-proxy") != "false" {
		return errors.New("Cilium must provide IPv4 L3/L4 without compulsory L7 interception")
	}
	sc := inv["storage.k8s.io/StorageClass//atlas-local-retain"]
	if sc["reclaimPolicy"] != "Retain" || sc["volumeBindingMode"] != "WaitForFirstConsumer" {
		return errors.New("unsafe local storage semantics")
	}
	ep := inv["gateway.envoyproxy.io/EnvoyProxy/"+p.gatewayNamespace()+"/development"]
	if field(ep, "spec", "ipFamily") != "IPv4" {
		return errors.New("Envoy listeners must use IPv4")
	}
	patches := slice(field(ep, "spec", "bootstrap", "jsonPatches"))
	if len(patches) != 1 || mapping(patches[0])["value"] != "V4_ONLY" || mapping(patches[0])["jsonPath"] != "$.static_resources.clusters[?(@.name == 'xds_cluster')]" || mapping(patches[0])["path"] != "/dns_lookup_family" {
		return errors.New("xDS DNS must be IPv4 only")
	}
	dns := inv["gateway.envoyproxy.io/BackendTrafficPolicy/"+p.gatewayNamespace()+"/ipv4-dns"]
	if field(dns, "spec", "dns", "lookupFamily") != "IPv4" {
		return errors.New("backend DNS must be IPv4 only")
	}
	deny := inv["networking.k8s.io/NetworkPolicy/"+p.namespace("workload-web")+"/default-deny"]
	if len(mapping(field(deny, "spec", "podSelector"))) != 0 || !contains(field(deny, "spec", "policyTypes"), "Ingress") || !contains(field(deny, "spec", "policyTypes"), "Egress") || len(slice(field(deny, "spec", "ingress"))) != 0 || len(slice(field(deny, "spec", "egress"))) != 0 {
		return errors.New("missing tenant default deny")
	}
	var kind Object
	if e := readJSON(filepath.Join(p.Root, inputDir, "bootstrap/kind.json"), &kind); e != nil {
		return e
	}
	if field(kind, "networking", "disableDefaultCNI") != true || field(kind, "networking", "ipFamily") != "ipv4" || field(kind, "networking", "apiServerAddress") != "127.0.0.1" {
		return errors.New("invalid Cilium-first Kind substrate")
	}
	return nil
}
