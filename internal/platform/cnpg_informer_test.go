package platform

import (
	"encoding/json"
	"github.com/snkio027/atlas/internal/platformnames"
	"strings"
	"testing"
)

func TestCNPGInstallationInformerClosure(t *testing.T) {
	all := operatorObjects(t)
	for _, informer := range []struct{ group, resource, label string }{{"discovery.k8s.io", "endpointslices", "EndpointSlice"}, {"", "services", "Service"}} {
		for _, mode := range []string{"valid", "missing-get", "missing-list", "missing-watch", "named-list", "named-watch", "create", "update", "patch", "delete", "deletecollection", "*", "foreign", "global"} {
			t.Run(informer.label+"/"+mode, func(t *testing.T) {
				data, err := json.Marshal(all)
				if err != nil {
					t.Fatal(err)
				}
				var objects []Object
				if err := json.Unmarshal(data, &objects); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, role := range objects {
					if role["kind"] != "Role" || metadata(role)["namespace"] != platformnames.CNPGOperator || metadata(role)["name"] != "cnpg" {
						continue
					}
					for _, value := range slice(role["rules"]) {
						rule := mapping(value)
						if !contains(rule["apiGroups"], informer.group) || !contains(rule["resources"], informer.resource) {
							continue
						}
						found = true
						if strings.HasPrefix(mode, "missing-") || strings.HasPrefix(mode, "named-") {
							verb := strings.SplitN(mode, "-", 2)[1]
							remaining := []any{}
							for _, v := range slice(rule["verbs"]) {
								if v != verb {
									remaining = append(remaining, v)
								}
							}
							rule["verbs"] = remaining
							if strings.HasPrefix(mode, "named-") {
								// Informer LIST/WATCH has no metadata.name field selector.
								role["rules"] = append(slice(role["rules"]), Object{"apiGroups": []any{informer.group}, "resources": []any{informer.resource}, "verbs": []any{verb}, "resourceNames": []any{"cnpg-webhook"}})
							}
						}
					}
				}
				if !found {
					t.Fatal("missing installation informer rule")
				}
				want := "requires installation " + informer.label
				if mode != "valid" && !strings.HasPrefix(mode, "missing-") && !strings.HasPrefix(mode, "named-") {
					ns, kind, bindKind, verb := platformnames.CNPGOperator, "Role", "RoleBinding", mode
					want = informer.label + " mutation"
					if mode == "foreign" {
						ns, verb, want = "project-foreign", "list", "foreign "+informer.label+" access"
					}
					if mode == "global" {
						ns, kind, bindKind, verb, want = "", "ClusterRole", "ClusterRoleBinding", "list", "foreign "+informer.label+" access"
					}
					objects = append(objects,
						Object{"kind": kind, "metadata": Object{"namespace": ns, "name": "extra-informer"}, "rules": []any{Object{"apiGroups": []any{informer.group}, "resources": []any{informer.resource}, "verbs": []any{verb}, "resourceNames": []any{"specific-object"}}}},
						Object{"kind": bindKind, "metadata": Object{"namespace": ns, "name": "extra-informer"}, "roleRef": Object{"kind": kind, "name": "extra-informer"}, "subjects": []any{Object{"kind": "ServiceAccount", "namespace": platformnames.CNPGOperator, "name": "cnpg"}}},
					)
				}
				err = ValidateOperatorPermissions(objects)
				if mode == "valid" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("expected %s, got %v", want, err)
				}
			})
		}
	}
}

// Certificate discovery is an existing authenticated grant, not a CNPG informer.
func TestCNPGPublicCertificateServiceBoundary(t *testing.T) {
	for _, mode := range []string{"valid", "unnamed", "wrong-name", "extra-name", "list", "watch", "update", "foreign", "wildcard-group"} {
		t.Run(mode, func(t *testing.T) {
			objects := operatorObjects(t)
			ns := platformnames.Secrets
			rule := Object{"apiGroups": []any{""}, "resources": []any{"services"}, "verbs": []any{"get"}, "resourceNames": []any{"sealed-secrets"}}
			switch mode {
			case "unnamed":
				delete(rule, "resourceNames")
			case "wrong-name":
				rule["resourceNames"] = []any{"other"}
			case "extra-name":
				rule["resourceNames"] = []any{"sealed-secrets", "other"}
			case "list", "watch", "update":
				rule["verbs"] = []any{"get", mode}
			case "foreign":
				ns = "project-foreign"
			case "wildcard-group":
				rule["apiGroups"] = []any{"*"}
			}
			objects = append(objects,
				Object{"kind": "Role", "metadata": Object{"namespace": ns, "name": "public-cert"}, "rules": []any{rule}},
				Object{"kind": "RoleBinding", "metadata": Object{"namespace": ns, "name": "public-cert"}, "roleRef": Object{"kind": "Role", "name": "public-cert"}, "subjects": []any{Object{"kind": "Group", "name": "system:authenticated"}}},
			)
			err := ValidateOperatorPermissions(objects)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "foreign Service access") {
				t.Fatalf("expected public discovery boundary rejection, got %v", err)
			}
		})
	}
}
