package platform

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/platformnames"
	"fmt"
	"path/filepath"
)

func (p *Project) namespace(unit string) string {
	if !p.currentLayout() {
		return unit
	}
	if unit == "envoy-gateway-system" {
		unit = "gateway"
	}
	return platformnames.Name(unit)
}
func namespaceObject(n platformnames.Namespace) Object {
	labels := Object{"kubernetes.io/metadata.name": n.Name, "atlas.local/class": n.Class, "atlas.local/domain": n.Domain, "atlas.local/owner": n.Owner}
	// Argo/cert-manager/gateway keep their existing admission baseline. This
	// layout does not silently tighten controller security profiles.
	if n.PodSecurity != "unchanged" {
		labels["pod-security.kubernetes.io/enforce"] = n.PodSecurity
	}
	if n.Unit == "workload-web" {
		labels["atlas.local/project"] = "platform-smoke"
		labels["atlas.io/gateway-access"] = "development"
	}
	return Object{"apiVersion": "v1", "kind": "Namespace", "metadata": Object{"name": n.Name, "labels": labels, "annotations": Object{"argocd.argoproj.io/sync-options": "Delete=false,Prune=false"}}}
}

var coreInputs = map[string]string{
	"foundation":            "gitops/platform/foundation/overlays/development/resources.json",
	"local-pki":             "gitops/platform/management/local-pki/overlays/development/resources.json",
	"edge":                  "gitops/platform/networking/edge/overlays/development/resources.json",
	"root":                  "gitops/root/overlays/development/resources.json",
	"workload-applications": "gitops/workloads/applications/overlays/development/resources.json",
	"web-smoke":             "gitops/workloads/web-smoke/overlays/development/resources.json",
}

func (p *Project) renderCoreInputs(files map[string][]byte) error {
	for name, path := range coreInputs {
		b, e := authored.Read(filepath.Join(p.Root, inputDir, "resources", name+".yaml"))
		if e != nil {
			return e
		}
		xs, e := decodeObjects(b)
		if e != nil {
			return fmt.Errorf("%s: %w", name, e)
		}
		if name == "foundation" {
			xs, e = namespaceDefinitions(name, xs)
			if e != nil {
				return e
			}
		}
		files[path] = encodedList(xs)
	}
	return nil
}
func encodedList(xs []Object) []byte {
	return encoded([]Object{{"apiVersion": "v1", "kind": "List", "items": xs}})
}

// Namespace definitions come from the one embedded layout; authored policies
// reference them, but cannot replace their declaration owner or lifecycle.
func namespaceDefinitions(owner string, xs []Object) ([]Object, error) {
	defs := map[string]platformnames.Namespace{}
	for _, n := range platformnames.Definitions() {
		if n.DeclarativeOwner == owner {
			defs[n.Name] = n
		}
	}
	seen := map[string]bool{}
	for i, o := range xs {
		if o["kind"] != "Namespace" {
			continue
		}
		name := str(metadata(o)["name"])
		if name == "local-path-storage" && owner == "foundation" {
			continue
		}
		n, ok := defs[name]
		if !ok || seen[name] {
			return nil, fmt.Errorf("namespace %s is not uniquely declared by %s", name, owner)
		}
		// Preserve reviewed authorization labels while deriving classification.
		replacement := namespaceObject(n)
		for k, v := range mapping(metadata(o)["labels"]) {
			if k == "atlas.io/gateway-access" || k == "pod-security.kubernetes.io/enforce-version" {
				mapping(metadata(replacement)["labels"])[k] = v
			}
		}
		xs[i] = replacement
		seen[name] = true
	}
	for name := range defs {
		if !seen[name] {
			return nil, fmt.Errorf("%s lacks namespace %s", owner, name)
		}
	}
	return xs, nil
}

func (p *Project) operatorNamespace(unit, legacy string) string {
	if p.currentLayout() {
		return platformnames.Name(unit)
	}
	return legacy
}

func (p *Project) projectNamespaceReader(xs []Object) error {
	var job *CapabilityJob
	for i := range p.Capabilities.Catalog.Jobs {
		j := &p.Capabilities.Catalog.Jobs[i]
		if j.Release == "sealed-secrets" {
			job = j
		}
	}
	if job == nil {
		return fmt.Errorf("missing namespace reader job")
	}
	matches := 0
	for _, o := range xs {
		if o["kind"] != "ClusterRole" || metadata(o)["name"] != "sealed-secrets-namespace-reader" {
			continue
		}
		rules := slice(o["rules"])
		if len(rules) != 1 || !contains(field(mapping(rules[0]), "resources"), "namespaces") || !contains(field(mapping(rules[0]), "verbs"), "get") {
			return fmt.Errorf("namespace reader shape changed")
		}
		mapping(rules[0])["resourceNames"] = p.watchedNamespaces(*job)
		matches++
	}
	if matches != 1 {
		return fmt.Errorf("namespace reader must be unique")
	}
	return nil
}
