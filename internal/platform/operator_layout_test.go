package platform

import (
	"atlas-refactor/internal/platformnames"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
