package platform

import (
	"encoding/json"
	"github.com/snkio027/atlas/internal/platformnames"
	"strings"
	"testing"
)

// Independent omission matrix for CNPG 1.30.1's installation control plane.
// An informer-only test cannot cover PKI writes, configuration and lease renewal.
func TestCNPGInstallationControlPlaneClosure(t *testing.T) {
	original := operatorObjects(t)
	for _, requirement := range []struct {
		group, resource string
		verbs           []string
	}{
		{"coordination.k8s.io", "leases", []string{"create", "get", "list", "update", "watch"}},
		{"", "configmaps", []string{"create", "delete", "get", "list", "patch", "update", "watch"}},
		{"", "secrets", []string{"create", "delete", "get", "list", "patch", "update", "watch"}},
		{"", "events", []string{"create", "patch"}},
	} {
		for _, verb := range requirement.verbs {
			t.Run(requirement.resource+"/"+verb, func(t *testing.T) {
				raw, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				var objects []Object
				if err := json.Unmarshal(raw, &objects); err != nil {
					t.Fatal(err)
				}
				removed := false
				for _, object := range objects {
					if object["kind"] != "Role" || metadata(object)["namespace"] != platformnames.CNPGOperator || metadata(object)["name"] != "cnpg" {
						continue
					}
					var rules []any
					for _, value := range slice(object["rules"]) {
						rule := mapping(value)
						if !contains(rule["apiGroups"], requirement.group) || !contains(rule["resources"], requirement.resource) {
							rules = append(rules, rule)
							continue
						}
						// Split combined configmaps/secrets rules so each case removes
						// only its target resource/verb, not another earlier requirement.
						for _, resource := range slice(rule["resources"]) {
							verbs := []any{}
							for _, v := range slice(rule["verbs"]) {
								if resource == requirement.resource && v == verb {
									removed = true
								} else {
									verbs = append(verbs, v)
								}
							}
							rules = append(rules, Object{"apiGroups": rule["apiGroups"], "resources": []any{resource}, "verbs": verbs})
						}
					}
					object["rules"] = rules
				}
				if !removed {
					t.Fatal("fixture missing required permission")
				}
				err = ValidateOperatorPermissions(objects)
				if err == nil || !strings.Contains(err.Error(), "requires installation "+requirement.resource+" "+verb) {
					t.Fatalf("missing %s/%s accepted: %v", requirement.resource, verb, err)
				}
			})
		}
	}
}
