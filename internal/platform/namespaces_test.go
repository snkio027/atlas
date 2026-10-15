package platform

import (
	"atlas-refactor/internal/platformnames"
	"bytes"
	"strings"
	"testing"
)

func TestCurrentTemplateNamespaceClosure(t *testing.T) {
	p := candidate(t)
	files, err := p.BundleFiles()
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{"default": true, "kube-system": true, "kube-public": true, "kube-node-lease": true}
	objects := []Object{}
	for path, b := range files {
		if !strings.HasPrefix(path, "gitops/") || strings.HasSuffix(path, "kustomization.yaml") {
			continue
		}
		xs, err := DecodeJSONManifests(b)
		if err != nil {
			t.Fatal(path, err)
		}
		for _, o := range xs {
			if o["kind"] == "Namespace" {
				declared[str(metadata(o)["name"])] = true
			}
			if o["kind"] == "Secret" || o["kind"] == "SealedSecret" {
				t.Fatalf("instance credential remains in new source template: %s", path)
			}
		}
		for _, old := range []string{"atlas-gateway", "atlas-secrets", "atlas-storage"} {
			if bytes.Contains(b, []byte(old)) {
				t.Fatalf("legacy namespace/reference %s in %s", old, path)
			}
		}
		objects = append(objects, xs...)
	}
	for _, ns := range []string{platformnames.Gateway, platformnames.Monitoring, platformnames.Secrets, platformnames.ObjectStorage} {
		if !declared[ns] {
			t.Fatalf("missing new namespace %s", ns)
		}
	}
	if declared["atlas-monitoring"] {
		t.Fatal("legacy monitoring Namespace remains")
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if name, ok := value.(string); ok && (key == "namespace" || key == "kubernetes.io/metadata.name" || key == "gateway.envoyproxy.io/owning-gateway-namespace") {
					if !declared[name] {
						t.Errorf("namespace reference %s=%s has no declared owner", key, name)
					}
				}
				walk(value)
			}
		case []any:
			for _, value := range x {
				walk(value)
			}
		}
	}
	for _, o := range objects {
		walk(o)
	}
	// Namespace changes must not rename the release or its selectors.
	for _, job := range p.Capabilities.Catalog.Jobs {
		if job.Release == "atlas-monitoring" && job.Namespace != platformnames.Monitoring {
			t.Fatal("monitoring release/namespace conflated")
		}
	}
	if err := p.ValidateCapabilityProjection(); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateCapabilityActivation(); err == nil {
		t.Fatal("unsealed templates authorize activation")
	}
}

func TestLayoutOwnershipAndCrossNamespaceSelectors(t *testing.T) {
	p := candidate(t)
	files, e := p.BundleFiles()
	if e != nil {
		t.Fatal(e)
	}
	namespaces := map[string]Object{}
	all := []Object{}
	for path, b := range files {
		if !strings.HasPrefix(path, "gitops/") || strings.HasSuffix(path, "kustomization.yaml") {
			continue
		}
		xs, e := DecodeJSONManifests(b)
		if e != nil {
			t.Fatal(path, e)
		}
		all = append(all, xs...)
		for _, o := range xs {
			if o["kind"] == "Namespace" {
				n := str(metadata(o)["name"])
				if namespaces[n] != nil {
					t.Fatal("duplicate namespace owner", n)
				}
				namespaces[n] = o
			}
		}
	}
	for _, n := range platformnames.Definitions() {
		o := namespaces[n.Name]
		if o == nil {
			t.Fatal("missing namespace", n.Name)
		}
		for k, want := range map[string]string{"atlas.local/class": n.Class, "atlas.local/domain": n.Domain, "atlas.local/owner": n.Owner} {
			if field(o, "metadata", "labels", k) != want {
				t.Fatal("classification differs", n.Name, k)
			}
		}
		options := str(field(o, "metadata", "annotations", "argocd.argoproj.io/sync-options"))
		if !strings.Contains(options, "Prune=false") || !strings.Contains(options, "Delete=false") {
			t.Fatal("namespace can be implicitly deleted", n.Name)
		}
	}
	if e := ValidateOperatorPermissions(all); e != nil {
		t.Fatal(e)
	}
	for _, o := range all {
		if o["kind"] == "NetworkPolicy" {
			for _, direction := range []string{"ingress", "egress"} {
				for _, raw := range slice(field(o, "spec", direction)) {
					rule := mapping(raw)
					key := "from"
					if direction == "egress" {
						key = "to"
					}
					for _, v := range slice(rule[key]) {
						peer := mapping(v)
						if sel := mapping(peer["namespaceSelector"]); sel != nil {
							ns := str(field(sel, "matchLabels", "kubernetes.io/metadata.name"))
							if ns != "" && platformnames.Legacy(ns) {
								t.Fatal("old namespace selector", identity(o), ns)
							}
							if ns != "" && peer["podSelector"] == nil {
								t.Fatal("namespace peer lacks same-peer Pod selector", identity(o))
							}
						}
					}
				}
			}
		}
	}
}
