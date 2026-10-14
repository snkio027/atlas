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
