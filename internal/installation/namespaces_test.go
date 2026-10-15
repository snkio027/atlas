package installation

import (
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/platformnames"
	"strings"
	"testing"
)

func TestProductRejectsLegacyAndMixedNamespaceLayouts(t *testing.T) {
	original := testProduct(t)
	for _, legacy := range []bool{false, true} {
		var p Product
		if err := Decode(JSON(original), &p); err != nil {
			t.Fatal(err)
		}
		for _, files := range []Files{p.Base, p.Full} {
			const path = "gitops/platform/foundation/overlays/development/resources.json"
			xs, err := platform.DecodeJSONManifests(files[path])
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range xs {
				if o["kind"] == "Namespace" && nested(o, "metadata", "name") == platformnames.Gateway {
					if legacy {
						mapping(o["metadata"])["name"] = "atlas-gateway"
					} else {
						xs = append(xs, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "atlas-gateway"}})
					}
					break
				}
			}
			files[path] = JSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": xs})
		}
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "namespace") {
			t.Fatalf("old/mixed package accepted: %v", err)
		}
	}
}

func TestProductBindsLayoutAndRejectsUndeclaredResourceNamespace(t *testing.T) {
	original := testProduct(t)
	for _, change := range []func(*Product){
		func(p *Product) { p.Schema = 1 },
		func(p *Product) { p.LayoutVersion = "unknown" },
		func(p *Product) { p.LayoutSHA256 = strings.Repeat("0", 64) },
		func(p *Product) {
			const path = "gitops/platform/management/local-pki/overlays/development/resources.json"
			xs, err := platform.DecodeJSONManifests(p.Full[path])
			if err != nil {
				t.Fatal(err)
			}
			mapping(xs[0]["metadata"])["namespace"] = "platform-undeclared"
			p.Full[path] = JSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": xs})
		},
	} {
		var p Product
		if err := Decode(JSON(original), &p); err != nil {
			t.Fatal(err)
		}
		change(&p)
		if p.Validate() == nil {
			t.Fatal("unbound layout/namespace accepted")
		}
	}
}
