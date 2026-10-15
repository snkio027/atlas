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
				mapping(raw)["namespaceSelector"] = Object{"matchLabels": Object{"kubernetes.io/metadata.name": "postgresql"}}
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
					image := p.Capabilities.Lock.Images["quay.io/strimzi/kafka:1.3.0-kafka-4.3.1"]
					if image == "" || !strings.Contains(str(env["value"]), "4.3.1=quay.io/strimzi/kafka:1.3.0-kafka-4.3.1") {
						return fmt.Errorf("Strimzi supported image map changed: %s", env["name"])
					}
					env["value"] = "4.3.1=" + image
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
