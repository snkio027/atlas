package platform

import (
	"atlas-refactor/internal/platformnames"
	"encoding/json"
	"strings"
	"testing"
)

func TestCNPGInstallationEndpointSliceInformer(t *testing.T) {
	all := operatorObjects(t)
	for _, mode := range []string{"valid", "missing-get", "missing-list", "missing-watch", "named-list", "named-watch", "create", "update", "patch", "delete", "deletecollection", "*", "foreign", "global"} {
		t.Run(mode, func(t *testing.T) {
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
					if !contains(rule["apiGroups"], "discovery.k8s.io") || !contains(rule["resources"], "endpointslices") {
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
							role["rules"] = append(slice(role["rules"]), Object{"apiGroups": []any{"discovery.k8s.io"}, "resources": []any{"endpointslices"}, "verbs": []any{verb}, "resourceNames": []any{"cnpg-webhook"}})
						}
					}
				}
			}
			if !found {
				t.Fatal("missing installation EndpointSlice informer rule")
			}
			want := "requires installation EndpointSlice"
			if mode != "valid" && !strings.HasPrefix(mode, "missing-") && !strings.HasPrefix(mode, "named-") {
				ns, kind, bindKind, verb := platformnames.CNPGOperator, "Role", "RoleBinding", mode
				want = "EndpointSlice mutation"
				if mode == "foreign" {
					ns, verb, want = "project-foreign", "list", "foreign EndpointSlice access"
				}
				if mode == "global" {
					ns, kind, bindKind, verb, want = "", "ClusterRole", "ClusterRoleBinding", "list", "foreign EndpointSlice access"
				}
				objects = append(objects,
					Object{"kind": kind, "metadata": Object{"namespace": ns, "name": "extra-endpointslice"}, "rules": []any{Object{"apiGroups": []any{"discovery.k8s.io"}, "resources": []any{"endpointslices"}, "verbs": []any{verb}, "resourceNames": []any{"specific-slice"}}}},
					Object{"kind": bindKind, "metadata": Object{"namespace": ns, "name": "extra-endpointslice"}, "roleRef": Object{"kind": kind, "name": "extra-endpointslice"}, "subjects": []any{Object{"kind": "ServiceAccount", "namespace": platformnames.CNPGOperator, "name": "cnpg"}}},
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
