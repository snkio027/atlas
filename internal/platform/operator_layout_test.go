package platform

import (
	"atlas-refactor/internal/platformnames"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func operatorObjects(t *testing.T) []Object {
	t.Helper()
	p := candidate(t)
	all := []Object{}
	for _, name := range []string{"postgresql-operator", "kafka-operator", "flink-operator"} {
		xs, e := p.capabilityObjects(name)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, xs...)
	}
	return all
}

func TestCNPGPKIDeploymentDiscovery(t *testing.T) {
	all := operatorObjects(t)
	for _, mode := range []string{"missing", "get-only", "named-list", "create", "update", "patch", "delete", "deletecollection", "*"} {
		t.Run(mode, func(t *testing.T) {
			raw, err := json.Marshal(all)
			if err != nil {
				t.Fatal(err)
			}
			var objects []Object
			if err := json.Unmarshal(raw, &objects); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, role := range objects {
				if role["kind"] != "Role" || metadata(role)["namespace"] != platformnames.CNPGOperator || metadata(role)["name"] != "cnpg" {
					continue
				}
				for i, rawRule := range slice(role["rules"]) {
					rule := mapping(rawRule)
					if !contains(rule["apiGroups"], "apps") || !contains(rule["resources"], "deployments") {
						continue
					}
					found = true
					switch mode {
					case "missing": // Exact permission omission observed during layout-r1 startup.
						rules := slice(role["rules"])
						role["rules"] = append(rules[:i], rules[i+1:]...)
					case "get-only":
						rule["verbs"] = []any{"get"}
					case "named-list":
						rule["resourceNames"] = []any{"cnpg"}
					default:
						// An additional binding can grant named writes even while the
						// original read-only Role looks correct. Evaluate their union.
						objects = append(objects,
							Object{"kind": "Role", "metadata": Object{"namespace": platformnames.CNPGOperator, "name": "extra"}, "rules": []any{Object{"apiGroups": []any{"apps"}, "resources": []any{"deployments"}, "verbs": []any{mode}, "resourceNames": []any{"cnpg"}}}},
							Object{"kind": "RoleBinding", "metadata": Object{"namespace": platformnames.CNPGOperator, "name": "extra"}, "roleRef": Object{"kind": "Role", "name": "extra"}, "subjects": []any{Object{"kind": "ServiceAccount", "namespace": platformnames.CNPGOperator, "name": "cnpg"}}},
						)
					}
					break
				}
				break
			}
			if !found {
				t.Fatal("rendered CNPG installation Role cannot discover its PKI owner")
			}
			want := "installation Deployment mutation"
			if mode == "missing" || mode == "get-only" || mode == "named-list" {
				want = "requires deployment discovery"
			}
			if err := ValidateOperatorPermissions(objects); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %s, got %v", want, err)
			}
		})
	}
}
func TestOperatorEffectivePermissions(t *testing.T) {
	all := operatorObjects(t)
	if e := ValidateOperatorPermissions(all); e != nil {
		t.Fatal(e)
	}
	for _, ns := range []string{platformnames.CNPGOperator, platformnames.StrimziOperator, platformnames.FlinkOperator} {
		t.Run(ns, func(t *testing.T) {
			var sa string
			for _, o := range all {
				if o["kind"] == "ServiceAccount" && metadata(o)["namespace"] == ns {
					sa = str(metadata(o)["name"])
					break
				}
			}
			wide := Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": Object{"name": "hidden-wide"}, "rules": []any{Object{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get"}}}}
			binding := Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": Object{"name": "extra"}, "roleRef": Object{"kind": "ClusterRole", "name": "hidden-wide"}, "subjects": []any{Object{"kind": "ServiceAccount", "namespace": ns, "name": sa}}}
			if e := ValidateOperatorPermissions(append(append([]Object{}, all...), wide, binding)); e == nil {
				t.Fatal("additive cluster secret access escaped")
			}
			binding["kind"] = "RoleBinding"
			metadata(binding)["namespace"] = "service-unlisted"
			mapping(slice(wide["rules"])[0])["resourceNames"] = []any{"specific-secret"}
			if e := ValidateOperatorPermissions(append(append([]Object{}, all...), wide, binding)); e == nil {
				t.Fatal("named Secret grant in an unlisted namespace escaped")
			}
		})
	}
}
func TestLockedOperatorProjectionRejectsChangedShape(t *testing.T) {
	helm := os.Getenv("ATLAS_TEST_HELM")
	if helm == "" {
		t.Skip("real locked Helm required")
	}
	p := candidate(t)
	p.Tools.Helm = helm
	p.Tools.YQ = filepath.Join(p.Root, ".state/tools/yq")
	if x := os.Getenv("ATLAS_TEST_YQ"); x != "" {
		p.Tools.YQ = x
	}
	if _, e := os.Stat(p.Tools.YQ); e != nil {
		p.Tools.YQ = "yq"
	}
	for _, j := range p.Capabilities.Catalog.Jobs {
		if j.Release != "cnpg" && j.Release != "strimzi" && j.Release != "flink-operator" {
			continue
		}
		t.Run(j.Release, func(t *testing.T) {
			args := []string{"template", j.Release, j.Chart, "--namespace", j.Namespace, "--include-crds", "--skip-tests", "--kube-version", p.Lock.Kubernetes}
			for _, v := range j.Values {
				path, e := p.inputPath(v)
				if e != nil {
					t.Fatal(e)
				}
				args = append(args, "--values", path)
			}
			raw, e := p.run(context.Background(), nil, helm, args...)
			if e != nil {
				t.Fatal(e)
			}
			original, e := p.yaml(context.Background(), raw)
			if e != nil {
				t.Fatal(e)
			}
			clone := func() []Object {
				b, _ := json.Marshal(original)
				var out []Object
				if e := json.Unmarshal(b, &out); e != nil {
					t.Fatal(e)
				}
				return out
			}
			if _, e = p.projectOperatorLayout(j, clone()); e != nil {
				t.Fatal(e)
			}
			for _, mode := range []string{"missing", "extra", "subject", "permission"} {
				xs := clone()
				changed := false
				for i, o := range xs {
					if mode == "missing" && o["kind"] == "RoleBinding" {
						xs = append(xs[:i], xs[i+1:]...)
						changed = true
						break
					}
					if mode == "extra" && o["kind"] == "RoleBinding" {
						xs = append(xs, o)
						changed = true
						break
					}
					if mode == "subject" && o["kind"] == "RoleBinding" {
						mapping(slice(o["subjects"])[0])["namespace"] = "project-foreign"
						changed = true
						break
					}
					if mode == "permission" && (o["kind"] == "Role" || o["kind"] == "ClusterRole") {
						mapping(slice(o["rules"])[0])["verbs"] = []any{"*"}
						changed = true
						break
					}
				}
				if !changed {
					t.Fatal("missing fault injection target", mode)
				}
				if _, e = p.projectOperatorLayout(j, xs); e == nil {
					t.Fatal("changed chart accepted", mode)
				}
			}
		})
	}
}
