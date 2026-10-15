package platform

import (
	"fmt"
	"strings"
)

func (p *Project) hasDataOperators() bool {
	for _, name := range []string{"postgresql-operator", "kafka-operator", "flink-operator"} {
		if _, ok := p.Capabilities.Catalog.Components[name]; ok {
			return true
		}
	}
	return false
}

// These are finite projections of the three locked charts, not a generic patch
// API. Their CRs remain Git-owned; operator-created runtime objects never enter
// the Argo inventory. A chart upgrade must re-review these projections.
func (p *Project) hardenDataOperator(release string, o Object) error {
	if release != "cnpg" && release != "strimzi" && release != "flink-operator" {
		return nil
	}
	name := str(metadata(o)["name"])
	if o["kind"] == "CustomResourceDefinition" {
		metadata(o)["annotations"] = Object{"argocd.argoproj.io/sync-options": "Delete=false,Prune=confirm"}
		// CustomResourceColumnDefinition.priority is an omitempty int32 in
		// the API. The locked Flink chart emits explicit zeroes; Argo's SSA
		// comparison retains those zeroes while the API response omits them,
		// causing an endless OutOfSync/self-heal loop. Canonicalize only this
		// field on these new data CRDs, never defaults inside their schemas.
		for _, version := range slice(field(o, "spec", "versions")) {
			for _, raw := range slice(field(mapping(version), "additionalPrinterColumns")) {
				column := mapping(raw)
				if priority, ok := column["priority"].(float64); ok && priority == 0 {
					delete(column, "priority")
				}
			}
		}
	}
	if release == "cnpg" {
		if o["kind"] == "ClusterRole" && name == "cnpg" {
			for _, raw := range slice(o["rules"]) {
				r := mapping(raw)
				if contains(r["apiGroups"], "admissionregistration.k8s.io") {
					r["resourceNames"] = []any{"cnpg-mutating-webhook-configuration", "cnpg-validating-webhook-configuration"}
				}
			}
		}
		if o["kind"] == "MutatingWebhookConfiguration" || o["kind"] == "ValidatingWebhookConfiguration" {
			for _, raw := range slice(o["webhooks"]) {
				mapping(raw)["namespaceSelector"] = Object{"matchLabels": Object{"kubernetes.io/metadata.name": p.namespace("postgresql")}}
			}
		}
	}
	if release == "strimzi" && o["kind"] == "ClusterRole" && name == "strimzi-cluster-operator-global" {
		// STRIMZI_CREATE_GLOBAL_RESOURCES=false: no NodePort/rack binding
		// creation. Keep only upstream read-only node/storage discovery.
		rules := []any{}
		for _, raw := range slice(o["rules"]) {
			if !contains(field(mapping(raw), "resources"), "clusterrolebindings") {
				rules = append(rules, raw)
			}
		}
		o["rules"] = rules
	}
	if o["kind"] != "Deployment" {
		return nil
	}
	pod := mapping(field(o, "spec", "template", "spec"))
	for _, raw := range slice(pod["containers"]) {
		c := mapping(raw)
		if release == "strimzi" {
			for _, item := range slice(c["env"]) {
				env := mapping(item)
				switch env["name"] {
				case "STRIMZI_KAFKA_IMAGES", "STRIMZI_KAFKA_CONNECT_IMAGES", "STRIMZI_KAFKA_MIRROR_MAKER_2_IMAGES":
					images, err := p.pinStrimziImages(str(env["value"]))
					if err != nil {
						return fmt.Errorf("%s: %w", env["name"], err)
					}
					env["value"] = images
				}
			}
		}
		if release == "flink-operator" {
			// The upstream chart has no writable tmp/log mounts for a read-only
			// root filesystem. Both operator and webhook need these paths.
			c["volumeMounts"] = append(slice(c["volumeMounts"]), Object{"name": "operator-tmp", "mountPath": "/tmp"}, Object{"name": "operator-logs", "mountPath": "/opt/flink/log"})
		}
	}
	if release == "flink-operator" {
		pod["volumes"] = append(slice(pod["volumes"]), Object{"name": "operator-tmp", "emptyDir": Object{}}, Object{"name": "operator-logs", "emptyDir": Object{}})
	}
	return nil
}

// Strimzi validates its complete supported-version catalog at startup, even
// when the only authored Kafka CR explicitly selects 4.3.1. Pin each genuine
// version image; narrowing the catalog, or aliasing old versions to a newer
// binary, does not restrict runtime authority and breaks the upstream contract.
func (p *Project) pinStrimziImages(value string) (string, error) {
	versions := []string{"4.2.0", "4.2.1", "4.2.2", "4.3.0", "4.3.1"}
	entries := strings.Fields(value)
	if len(entries) != len(versions) {
		return "", fmt.Errorf("Strimzi 1.3.0 requires its complete supported image map")
	}
	seen := map[string]string{}
	for _, entry := range entries {
		version, image, ok := strings.Cut(entry, "=")
		if !ok || seen[version] != "" {
			return "", fmt.Errorf("invalid or duplicate Strimzi image entry")
		}
		seen[version] = image
	}
	result := make([]string, 0, len(versions))
	for _, version := range versions {
		source := "quay.io/strimzi/kafka:1.3.0-kafka-" + version
		locked := p.Capabilities.Lock.Images[source]
		if locked == "" || !pinnedImage.MatchString(locked) || !strings.HasPrefix(locked, source+"@sha256:") || (seen[version] != source && seen[version] != locked) {
			return "", fmt.Errorf("Strimzi image missing, changed or unpinned for version %s", version)
		}
		result = append(result, version+"="+locked)
	}
	return strings.Join(result, "\n"), nil
}
