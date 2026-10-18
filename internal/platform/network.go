package platform

import (
	"atlas-refactor/internal/developmentprofile"
	"atlas-refactor/internal/platformnames"
	"errors"
	"fmt"
)

// labelMatch covers this profile's exact namespace identity selectors. New
// match-expression policies require an explicit extension, not silent acceptance.
func labelMatch(selector, labels Object) bool {
	want := mapping(selector["matchLabels"])
	if len(want) == 0 || len(slice(selector["matchExpressions"])) != 0 {
		return false
	}
	for key, value := range want {
		if labels[key] != value {
			return false
		}
	}
	return true
}
func (p *Project) gatewayNamespace() string {
	// OT-1 reads fixed historical commits, never the current installation layout.
	if p.Config.Revision == developmentprofile.OT1Revision {
		return "atlas-gateway"
	}
	if p.currentLayout() {
		return platformnames.Gateway
	}
	return "gateway"
}

func checkRoutes(inv map[string]Object, gatewayNamespace string) error {
	gw := inv["gateway.networking.k8s.io/Gateway/"+gatewayNamespace+"/development"]
	smoke := "workload-web"
	if gatewayNamespace == platformnames.Gateway {
		smoke = platformnames.Smoke
	}
	ns := inv["/Namespace//"+smoke]
	listeners := map[string]Object{}
	for _, v := range slice(field(gw, "spec", "listeners")) {
		l := mapping(v)
		listeners[str(l["name"])] = l
		namespaces := mapping(field(l, "allowedRoutes", "namespaces"))
		if gatewayNamespace == platformnames.Gateway && field(namespaces, "selector", "matchLabels", "kubernetes.io/metadata.name") != smoke {
			return errors.New("Gateway must bind the exact smoke project, not a shared access label alone")
		}
		if namespaces["from"] != "Selector" || !labelMatch(mapping(namespaces["selector"]), mapping(metadata(ns)["labels"])) {
			return errors.New("Gateway must select the authorized workload namespace")
		}
		for _, o := range inv {
			if o["kind"] == "Namespace" && metadata(o)["name"] != smoke && labelMatch(mapping(namespaces["selector"]), mapping(metadata(o)["labels"])) {
				return errors.New("Gateway selector admits a platform namespace")
			}
		}
	}
	https := listeners["https"]
	if len(listeners) != 2 || listeners["http"]["protocol"] != "HTTP" || https["protocol"] != "HTTPS" || https["port"] != float64(443) || field(https, "tls", "mode") != "Terminate" {
		return errors.New("missing reviewed HTTP/HTTPS listeners")
	}
	refs := slice(field(https, "tls", "certificateRefs"))
	if len(refs) != 1 || mapping(refs[0])["name"] != "web-tls" || mapping(refs[0])["kind"] != "Secret" {
		return errors.New("HTTPS must use the runtime certificate Secret")
	}
	cert := inv["cert-manager.io/Certificate/"+gatewayNamespace+"/web-tls"]
	if field(cert, "spec", "secretName") != "web-tls" || !contains(field(cert, "spec", "dnsNames"), "web.atlas.test") {
		return errors.New("TLS certificate does not cover the development hostname")
	}
	count := 0
	for _, o := range inv {
		if o["kind"] != "HTTPRoute" {
			continue
		}
		count++
		for _, v := range slice(field(o, "spec", "parentRefs")) {
			ref := mapping(v)
			listener := listeners[str(ref["sectionName"])]
			if ref["name"] != metadata(gw)["name"] || ref["namespace"] != gatewayNamespace || listener == nil {
				return fmt.Errorf("route %s has an unapproved parent", identity(o))
			}
			if !contains(field(o, "spec", "hostnames"), str(listener["hostname"])) {
				return errors.New("route hostname does not match listener")
			}
		}
	}
	if count != 2 {
		return errors.New("HTTP redirect and HTTPS routes are required")
	}
	ep := inv["gateway.envoyproxy.io/EnvoyProxy/"+gatewayNamespace+"/development"]
	ports := slice(field(ep, "spec", "provider", "kubernetes", "envoyService", "patch", "value", "spec", "ports"))
	expected := map[float64]float64{80: 30080, 443: 30443}
	if len(ports) != 2 {
		return errors.New("expected two controller-owned Service ports")
	}
	for _, v := range ports {
		m := mapping(v)
		port, _ := m["port"].(float64)
		if m["nodePort"] != expected[port] {
			return errors.New("NodePort differs from reviewed ingress mapping")
		}
	}
	return nil
}
