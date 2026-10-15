package platformcheck

import (
	"atlas-refactor/internal/platformnames"
	"errors"
	"fmt"
	"strings"
)

type Pod struct {
	Metadata struct {
		Name, Namespace string
		Labels          map[string]string
	}
	Spec   struct{ NodeName string }
	Status struct {
		Phase      string
		Conditions []struct{ Type, Status string }
	}
}

func VerifyPods(pods []Pod, cluster, gatewayNamespace string) error {
	web, proxy := false, false
	smoke, proxyNS := "workload-web", "envoy-gateway-system"
	if gatewayNamespace == platformnames.Gateway {
		smoke, proxyNS = platformnames.Smoke, platformnames.Gateway
	}
	for _, p := range pods {
		if p.Status.Phase == "Succeeded" {
			continue
		}
		ready := false
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
		if p.Status.Phase != "Running" || !ready {
			if p.Status.Phase == "Pending" || p.Status.Phase == "Running" {
				return PendingPod{p.Metadata.Namespace, p.Metadata.Name}
			}
			return fmt.Errorf("pod failed/unknown: %s/%s", p.Metadata.Namespace, p.Metadata.Name)
		}
		if p.Metadata.Namespace == smoke {
			if p.Spec.NodeName != cluster+"-worker3" {
				return fmt.Errorf("workload Pod outside data node: %s", p.Metadata.Name)
			}
			web = true
		}
		if p.Metadata.Namespace == proxyNS && strings.HasPrefix(p.Metadata.Name, "envoy-"+gatewayNamespace+"-") &&
			p.Metadata.Labels["app.kubernetes.io/component"] == "proxy" &&
			p.Metadata.Labels["gateway.envoyproxy.io/owning-gateway-name"] == "development" &&
			p.Metadata.Labels["gateway.envoyproxy.io/owning-gateway-namespace"] == gatewayNamespace {
			if p.Spec.NodeName != cluster+"-worker" {
				return fmt.Errorf("gateway Pod outside gateway node: %s", p.Metadata.Name)
			}
			proxy = true
		}
	}
	if !web || !proxy {
		return errors.New("gateway/data placement failed")
	}
	return nil
}

// PendingPod identifies ordinary startup without weakening placement or failure
// checks. Callers decide whether their command is observation or a bounded wait.
type PendingPod struct{ Namespace, Name string }

func (e PendingPod) Error() string { return fmt.Sprintf("pod not Ready: %s/%s", e.Namespace, e.Name) }
