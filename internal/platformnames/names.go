// Package platformnames defines the namespace identities of new installations.
// Helm release names, resource names and historical ceremony inputs are separate.
package platformnames

import "fmt"

const (
	Gateway       = "gateway"
	Monitoring    = "monitoring"
	Secrets       = "sealed-secrets"
	ObjectStorage = "object-storage"
	S3Address     = "seaweedfs." + ObjectStorage + ".svc.cluster.local:8333"
)

// Reserved covers the built-in platform even before capabilities are enabled.
// The compiler also rejects namespaces declared by the actual deployment base.
func Reserved(name string) bool {
	switch name {
	case "postgresql", "redis", "kafka", "flink", "logging", "default", "argocd", "cert-manager", "envoy-gateway-system", "local-path-storage", "workload-web", Gateway, Monitoring, Secrets, ObjectStorage:
		return true
	}
	return false
}

// ValidateDefinitions prevents a new binary from operating on an old or mixed
// installation package. It is not a migration or an alias resolver.
func ValidateDefinitions(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || Legacy(name) {
			return fmt.Errorf("unsupported/duplicate platform namespace %q; use the instance-bound version or a reviewed migration", name)
		}
		seen[name] = true
	}
	for _, name := range []string{Gateway, Monitoring, Secrets, ObjectStorage} {
		if !seen[name] {
			return fmt.Errorf("installation lacks current platform namespace %q; old packages require their bound version", name)
		}
	}
	return nil
}

func Legacy(name string) bool {
	switch name {
	case "atlas-gateway", "atlas-monitoring", "atlas-secrets", "atlas-storage":
		return true
	}
	return false
}
