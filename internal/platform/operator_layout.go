package platform

import (
	"atlas-refactor/internal/platformnames"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Finite adapters for the exact locked charts. Inspect the complete input before
// transforming anything: changed chart/RBAC/env structures cannot fall back to
// broad upstream defaults. The ordinary hardening then pins images/webhooks.
func (p *Project) projectOperatorLayout(j CapabilityJob, objects []Object) ([]Object, error) {
	if !p.currentLayout() {
		return objects, nil
	}
	var chart, digest, operator, runtime, deploy, watch, oldWatch string
	switch j.Release {
	case "cnpg":
		chart = "vendor/platform/cloudnative-pg-0.29.1.tgz"
		digest = "b53d3991fe84bcf38767e7702cae78666265427a127a26fff168ab4207d2b1df"
		operator = platformnames.CNPGOperator
		runtime = platformnames.PostgreSQL
		deploy = "cnpg"
		watch = "WATCH_NAMESPACE"
		oldWatch = operator
	case "strimzi":
		chart = "vendor/platform/strimzi-kafka-operator-helm-3-chart-1.3.0.tgz"
		digest = "42677cec73d2122f4a395d9891bbccfae18ec16b3582174d49ca04695f351192"
		operator = platformnames.StrimziOperator
		runtime = platformnames.Kafka
		deploy = "strimzi-cluster-operator"
		watch = "STRIMZI_NAMESPACE"
		oldWatch = operator + "," + runtime
	case "flink-operator":
		chart = "vendor/platform/flink-kubernetes-operator-1.16.0-helm.tgz"
		digest = "8afdca38d3104f47a5c59f63886f9153d0f63fc0a0ce7979c79357f9571d4fe5"
		operator = platformnames.FlinkOperator
		runtime = platformnames.Flink
		deploy = "flink-kubernetes-operator"
	default:
		return objects, nil
	}
	if j.Chart != chart || j.Namespace != operator || (digest != "" && p.Capabilities.Lock.Artifacts[chart].SHA256 != digest) {
		return nil, errors.New("operator layout requires its exact reviewed chart and namespace")
	}
	// Hash the entire upstream RBAC/SA set, including all additive bindings.
	pins := map[string]string{"cnpg": "72ce1d84f7e6ddbeef45f4f841cd764aef25fcf0852ce6838ca7eb0fc77596a5", "strimzi": "25ea755276fa58c9d2dc9e13235ea29e425939dbdd2ec8f6edf3ee7f395c8b9c", "flink-operator": "275f0c722b035b1c2aecbd5387a85f13b1beca651ec48c2ad8729e86717dc3e6"}
	rb := []Object{}
	for _, o := range objects {
		switch o["kind"] {
		case "Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding", "ServiceAccount":
			rb = append(rb, o)
		}
	}
	sort.Slice(rb, func(i, j int) bool { return identity(rb[i]) < identity(rb[j]) })
	raw, err := json.Marshal(rb)
	if err != nil {
		return nil, err
	}
	if hash(raw) != pins[j.Release] {
		return nil, fmt.Errorf("%s upstream RBAC shape differs from reviewed projection", j.Release)
	}
	selectOne := func(kind, ns, name string) (Object, error) {
		var out Object
		for _, o := range objects {
			if o["kind"] == kind && metadata(o)["name"] == name && str(metadata(o)["namespace"]) == ns {
				if out != nil {
					return nil, errors.New("duplicate operator projection target")
				}
				out = o
			}
		}
		if out == nil {
			return nil, fmt.Errorf("missing operator projection target %s/%s/%s", kind, ns, name)
		}
		return out, nil
	}
	d, e := selectOne("Deployment", operator, deploy)
	if e != nil {
		return nil, e
	}
	if watch != "" {
		found := 0
		for _, c := range slice(field(d, "spec", "template", "spec", "containers")) {
			for _, v := range slice(field(mapping(c), "env")) {
				env := mapping(v)
				if env["name"] == watch {
					if env["value"] != oldWatch {
						return nil, fmt.Errorf("unexpected %s before layout projection", watch)
					}
					found++
				}
			}
		}
		if found != 1 {
			return nil, errors.New("watch projection must match exactly one environment variable")
		}
	}
	if j.Release == "cnpg" {
		role, e := selectOne("Role", operator, "cnpg")
		if e != nil {
			return nil, e
		}
		binding, e := selectOne("RoleBinding", operator, "cnpg")
		if e != nil {
			return nil, e
		}
		if len(slice(role["rules"])) != 20 || field(binding, "roleRef", "kind") != "Role" || field(binding, "roleRef", "name") != "cnpg" || len(slice(binding["subjects"])) != 1 || field(mapping(slice(binding["subjects"])[0]), "namespace") != operator {
			return nil, errors.New("CNPG RBAC structure changed")
		}
		// Original full runtime rules move to the service. The installation domain
		// retains only leader election, configuration, TLS secrets and events.
		var own Object
		b, _ := json.Marshal(role)
		_ = json.Unmarshal(b, &own)
		own["rules"] = []any{
			Object{"apiGroups": []any{"coordination.k8s.io"}, "resources": []any{"leases"}, "verbs": []any{"create", "get", "list", "update", "watch"}},
			Object{"apiGroups": []any{""}, "resources": []any{"configmaps", "secrets"}, "verbs": []any{"create", "delete", "get", "list", "patch", "update", "watch"}},
			Object{"apiGroups": []any{""}, "resources": []any{"events"}, "verbs": []any{"create", "patch"}},
		}
		var ownBinding Object
		b, _ = json.Marshal(binding)
		_ = json.Unmarshal(b, &ownBinding)
		metadata(role)["namespace"] = runtime
		metadata(binding)["namespace"] = runtime
		objects = append(objects, own, ownBinding)
	}
	if j.Release == "strimzi" {
		b, e := selectOne("RoleBinding", operator, "strimzi-cluster-operator")
		if e != nil {
			return nil, e
		}
		target, e := selectOne("RoleBinding", runtime, "strimzi-cluster-operator")
		if e != nil {
			return nil, e
		}
		for _, binding := range []Object{b, target} {
			if field(binding, "roleRef", "name") != "strimzi-cluster-operator-namespaced" || len(slice(binding["subjects"])) != 1 || field(mapping(slice(binding["subjects"])[0]), "namespace") != operator {
				return nil, errors.New("Strimzi runtime binding changed")
			}
		}
		// Upstream always watches its installation namespace too. Remove only the
		// three instance/delegation bindings; only leader-election stays there.
		filtered := []Object{}
		for _, o := range objects {
			if o["kind"] == "RoleBinding" && metadata(o)["namespace"] == operator && (metadata(o)["name"] == "strimzi-cluster-operator" || metadata(o)["name"] == "strimzi-cluster-operator-watched" || metadata(o)["name"] == "strimzi-cluster-operator-entity-operator-delegation") {
				continue
			}
			filtered = append(filtered, o)
		}
		objects = filtered
	}
	if j.Release == "flink-operator" {
		for _, ns := range []string{operator, runtime} {
			if _, e := selectOne("RoleBinding", ns, "flink-operator-role-binding"); e != nil {
				return nil, e
			}
		}
		cm, e := selectOne("ConfigMap", operator, "flink-operator-config")
		if e != nil {
			return nil, e
		}
		if countExactWatch(str(field(cm, "data", "config.yaml")), runtime) != 1 {
			return nil, errors.New("Flink watch configuration differs")
		}
	}
	if watch != "" {
		for _, c := range slice(field(d, "spec", "template", "spec", "containers")) {
			for _, v := range slice(field(mapping(c), "env")) {
				env := mapping(v)
				if env["name"] == watch {
					env["value"] = runtime
				}
			}
		}
	}
	return objects, nil
}

func countExactWatch(config, runtime string) int {
	count := 0
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "kubernetes.operator.watched.namespaces:") {
			if strings.TrimSpace(line) != "kubernetes.operator.watched.namespaces: "+runtime {
				return -1
			}
			count++
		}
	}
	return count
}
