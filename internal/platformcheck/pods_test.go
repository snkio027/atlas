package platformcheck

import (
	"atlas-refactor/internal/platformnames"
	"encoding/json"
	"testing"
)

func TestNewInstallationRequiresCurrentGatewayIdentity(t *testing.T) {
	var pods []Pod
	if err := json.Unmarshal([]byte(`[
 {"metadata":{"name":"web-1","namespace":"project-platform-smoke"},"spec":{"nodeName":"test-worker3"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"envoy-platform-gateway-development-1","namespace":"platform-gateway","labels":{"app.kubernetes.io/component":"proxy","gateway.envoyproxy.io/owning-gateway-name":"development","gateway.envoyproxy.io/owning-gateway-namespace":"platform-gateway"}},"spec":{"nodeName":"test-worker"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}
 ]`), &pods); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPods(pods, "test", platformnames.Gateway); err != nil {
		t.Fatal(err)
	}
	pods[1].Metadata.Name = "envoy-atlas-gateway-development-1"
	pods[1].Metadata.Labels["gateway.envoyproxy.io/owning-gateway-namespace"] = "atlas-gateway"
	if err := VerifyPods(pods, "test", platformnames.Gateway); err == nil {
		t.Fatal("old proxy accepted as the new Gateway")
	}
	pods[0].Metadata.Namespace = "workload-web"
	pods[1].Metadata.Namespace = "envoy-gateway-system"
	if err := VerifyPods(pods, "test", "atlas-gateway"); err != nil {
		t.Fatal("explicit frozen legacy profile changed:", err)
	}
}
