package platform

import (
	"atlas-refactor/internal/platformnames"
	"strings"
	"testing"
)

func TestSmokeGatewayRequiresExactProjectAlongsideAccessLabel(t *testing.T) {
	p := candidate(t)
	files, err := p.BundleFiles()
	if err != nil {
		t.Fatal(err)
	}
	inv := map[string]Object{}
	for name, b := range files {
		if !strings.HasPrefix(name, "gitops/") || strings.HasSuffix(name, "kustomization.yaml") {
			continue
		}
		xs, err := DecodeJSONManifests(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range xs {
			inv[identity(o)] = o
		}
	}
	// A second Project has the shared access label too; this must not authorize
	// it to use the platform smoke listener.
	inv["/Namespace//project-other"] = Object{"kind": "Namespace", "metadata": Object{"name": "project-other", "labels": Object{"atlas.io/gateway-access": "development", "kubernetes.io/metadata.name": "project-other"}}}
	if err := checkRoutes(inv, platformnames.Gateway); err != nil {
		t.Fatal(err)
	}
	gateway := inv["gateway.networking.k8s.io/Gateway/"+platformnames.Gateway+"/development"]
	for _, v := range slice(field(gateway, "spec", "listeners")) {
		delete(mapping(field(mapping(v), "allowedRoutes", "namespaces", "selector", "matchLabels")), "kubernetes.io/metadata.name")
	}
	if checkRoutes(inv, platformnames.Gateway) == nil {
		t.Fatal("shared access label broadened smoke listener")
	}
}
