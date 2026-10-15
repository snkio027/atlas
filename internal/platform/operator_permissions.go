package platform

import (
	"atlas-refactor/internal/platformnames"
	"fmt"
	"strings"
)

// ValidateOperatorPermissions evaluates additive Role/ClusterRole bindings for
// the three reviewed operator accounts. It is a static preflight, not a claim
// about live admission or RBAC. Unknown/aggregated role references fail closed.
func ValidateOperatorPermissions(objects []Object) error {
	roles := map[string]Object{}
	for _, o := range objects {
		if o["kind"] == "Role" || o["kind"] == "ClusterRole" {
			roles[str(o["kind"])+"/"+str(metadata(o)["namespace"])+"/"+str(metadata(o)["name"])] = o
		}
	}
	for _, op := range []struct {
		ns, sa, runtime, group, resource string
		ownSecrets                       bool
	}{
		{platformnames.CNPGOperator, "cnpg", platformnames.PostgreSQL, "postgresql.cnpg.io", "clusters", true},
		{platformnames.StrimziOperator, "strimzi-cluster-operator", platformnames.Kafka, "kafka.strimzi.io", "kafkas", false},
		{platformnames.FlinkOperator, "flink-operator", platformnames.Flink, "flink.apache.org", "flinkdeployments", false},
	} {
		present := false
		for _, o := range objects {
			present = present || o["kind"] == "ServiceAccount" && metadata(o)["namespace"] == op.ns && metadata(o)["name"] == op.sa
		}
		if !present {
			continue
		}
		type grant struct {
			ns   string
			rule Object
		}
		grants := []grant{}
		for _, b := range objects {
			if b["kind"] != "RoleBinding" && b["kind"] != "ClusterRoleBinding" {
				continue
			}
			member := false
			for _, raw := range slice(b["subjects"]) {
				sub := mapping(raw)
				name := str(sub["name"])
				member = member || sub["kind"] == "ServiceAccount" && sub["namespace"] == op.ns && name == op.sa || sub["kind"] == "User" && name == "system:serviceaccount:"+op.ns+":"+op.sa || sub["kind"] == "Group" && (name == "system:authenticated" || name == "system:serviceaccounts" || name == "system:serviceaccounts:"+op.ns)
			}
			if !member {
				continue
			}
			ns := str(metadata(b)["namespace"])
			ref := mapping(b["roleRef"])
			roleNS := ns
			if ref["kind"] == "ClusterRole" {
				roleNS = ""
			}
			role := roles[str(ref["kind"])+"/"+roleNS+"/"+str(ref["name"])]
			if role == nil || role["aggregationRule"] != nil {
				return fmt.Errorf("unresolved/aggregated effective role for %s", op.sa)
			}
			if b["kind"] == "ClusterRoleBinding" {
				ns = "*"
			}
			for _, r := range slice(role["rules"]) {
				grants = append(grants, grant{ns, mapping(r)})
			}
		}
		allowed := func(ns, group, resource, verb, name string) bool {
			for _, g := range grants {
				r := g.rule
				if (g.ns == ns || g.ns == "*") && (contains(r["apiGroups"], group) || contains(r["apiGroups"], "*")) && (contains(r["resources"], resource) || contains(r["resources"], "*")) && (contains(r["verbs"], verb) || contains(r["verbs"], "*")) && (len(slice(r["resourceNames"])) == 0 || contains(r["resourceNames"], name)) {
					return true
				}
			}
			return false
		}
		// Check the whole grant, not a sample Secret/CR name. A binding in an
		// unlisted namespace or a resourceNames-restricted grant is still access.
		for _, g := range grants {
			if g.ns == op.runtime || g.ns == op.ns {
				continue
			}
			r := g.rule
			secret := (contains(r["apiGroups"], "") || contains(r["apiGroups"], "*")) && (contains(r["resources"], "secrets") || contains(r["resources"], "*"))
			instance := false
			if contains(r["apiGroups"], op.group) || contains(r["apiGroups"], "*") {
				for _, raw := range slice(r["resources"]) {
					resource := str(raw)
					instance = instance || resource == "*" || resource == op.resource || strings.HasPrefix(resource, op.resource+"/")
				}
			}
			if (secret || instance) && len(slice(r["verbs"])) != 0 {
				return fmt.Errorf("operator %s has foreign sensitive grant in %s", op.sa, g.ns)
			}
		}
		for _, q := range []struct {
			ns, group, resource, verb, name string
			want                            bool
		}{
			{op.runtime, "", "secrets", "get", "fixture", true}, {op.runtime, "", "pods", "create", "", true},
			{op.runtime, op.group, op.resource + "/status", "update", "fixture", true},
			{op.ns, "coordination.k8s.io", "leases", "create", "", true},
			{op.ns, "", "secrets", "get", "fixture", op.ownSecrets},
			{op.ns, op.group, op.resource, "create", "", false},
		} {
			if allowed(q.ns, q.group, q.resource, q.verb, q.name) != q.want {
				return fmt.Errorf("operator %s effective permission mismatch: %s %s/%s in %s (want %v)", op.sa, q.verb, q.group, q.resource, q.ns, q.want)
			}
		}
		for _, ns := range []string{platformnames.Argo, platformnames.Monitoring, platformnames.Secrets, platformnames.Gateway, "project-foreign", "default"} {
			for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
				if allowed(ns, "", "secrets", verb, "fixture") || allowed(ns, op.group, op.resource, verb, "fixture") {
					return fmt.Errorf("operator %s has foreign access in %s", op.sa, ns)
				}
			}
		}
		for _, resource := range []string{"clusterroles", "clusterrolebindings"} {
			for _, verb := range []string{"create", "update", "patch", "delete", "bind", "escalate"} {
				if allowed("", "rbac.authorization.k8s.io", resource, verb, "") {
					return fmt.Errorf("operator %s has global RBAC mutation", op.sa)
				}
			}
		}
	}
	return nil
}
