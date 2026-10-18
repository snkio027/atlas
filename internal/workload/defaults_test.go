package workload

import (
	"atlas-refactor/internal/platform"
	"bytes"
	"strings"
	"testing"
)

func TestDefaultStabilityRejectsRegressedRoutes(t *testing.T) {
	c, m := compileFixture(t)
	result, err := Compile(c, m, "consumer", artifactFixture(c, m))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := InventoryOf(c.Base, c.ResourceModel)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, route, missing, field string }{
		{"parent", "web-api-http", "group", "parentRefs[0].group"},
		{"redirect", "web-api-http", "matches", "rules[0].matches"},
		{"backend", "web-api-https", "weight", "backendRefs[0].weight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := Files{}
			for path, raw := range result.Files {
				files[path] = append([]byte(nil), raw...)
			}
			path := "gitops/workloads/projects/demo/web-api/resources.json"
			err := edit(files, path, func(xs []Object) ([]Object, error) {
				route, e := find(xs, "HTTPRoute", "project-demo", tc.route)
				if e != nil {
					return nil, e
				}
				rule := obj(arr(val(route, "spec", "rules"))[0])
				switch tc.name {
				case "parent":
					delete(obj(arr(val(route, "spec", "parentRefs"))[0]), tc.missing)
				case "redirect":
					delete(rule, tc.missing)
				case "backend":
					delete(obj(arr(rule["backendRefs"])[0]), tc.missing)
				}
				return xs, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Type/scope validation alone accepts it.
			inv, err := InventoryOf(files, c.ResourceModel)
			if err != nil {
				t.Fatal(err)
			}
			pristine := platform.BundleDigest(files)
			err = validateDefaultStability(c, files, baseline, inv)
			if platform.BundleDigest(files) != pristine {
				t.Fatal("validation rewrote generated output")
			}
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("schema-valid default omission escaped: %v", err)
			}
		})
	}
}

func TestCompileRejectsSourceSchemaDifferentFromProduct(t *testing.T) {
	c, m := compileFixture(t)
	typ, err := c.ResourceModel.Resolve(Object{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway"})
	if err != nil {
		t.Fatal(err)
	}
	// The checkout is a valid but different schema. The installed D1 tree is
	// the defaulting authority for this compilation, not that newer checkout.
	obj(val(typ.Schema, "properties", "spec", "properties"))["future"] = Object{"type": "string", "default": "changed"}
	result, err := Compile(c, m, "infrastructure", nil)
	if err == nil || !strings.Contains(err.Error(), "CRD schema differs from immutable product base") || result.Files != nil {
		t.Fatalf("accepted mismatched schema: %v", err)
	}
}

func TestInfrastructurePreflightChecksFutureConsumerDefaults(t *testing.T) {
	c, m := compileFixture(t)
	typ, err := c.ResourceModel.Resolve(Object{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute"})
	if err != nil {
		t.Fatal(err)
	}
	obj(val(typ.Schema, "properties", "spec", "properties", "rules", "items", "properties", "timeouts"))["default"] = Object{"request": "30s"}
	// Model and product agree on this hypothetical future default. Infrastructure
	// contains no new Workload route, but planning must still reject the omission
	// before image/credential preparation or the first Git publication.
	matched := false
	for path, raw := range c.Base {
		if !strings.HasPrefix(path, "gitops/") || !bytes.Contains(raw, []byte("httproutes.gateway.networking.k8s.io")) {
			continue
		}
		xs, e := objects(raw)
		if e != nil {
			t.Fatal(e)
		}
		for _, o := range xs {
			if o["kind"] != "CustomResourceDefinition" || meta(o)["name"] != "httproutes.gateway.networking.k8s.io" {
				continue
			}
			for _, v := range arr(val(o, "spec", "versions")) {
				version := obj(v)
				if version["name"] == "v1" {
					obj(version["schema"])["openAPIV3Schema"] = clone(typ.Schema)
					matched = true
				}
			}
		}
		c.Base[path] = list(xs)
	}
	if !matched {
		t.Fatal("missing locked HTTPRoute definition")
	}
	for _, phase := range []string{"permissions", "project", "infrastructure"} {
		result, err := Compile(c, m, phase, nil)
		if err == nil || !strings.Contains(err.Error(), "$.spec.rules[0].timeouts") || result.Files != nil {
			t.Fatalf("%s: consumer defect escaped before publication: %v", phase, err)
		}
	}
}

func TestInfrastructurePreflightDoesNotReleaseConsumers(t *testing.T) {
	c, m := compileFixture(t)
	before := platform.BundleDigest(c.Base)
	r, err := Compile(c, m, "infrastructure", nil)
	if err != nil {
		t.Fatal(err)
	}
	for path := range r.Files {
		if strings.HasPrefix(path, "gitops/workloads/projects/") || strings.HasPrefix(path, "gitops/platform/bindings/") {
			t.Fatalf("consumer preflight escaped into infrastructure output: %s", path)
		}
	}
	for _, path := range []string{WorkloadAppsPath, CredentialsPath, StoragePath} {
		if !bytes.Equal(c.Base[path], r.Files[path]) {
			t.Fatalf("consumer preflight changed %s", path)
		}
	}
	if platform.BundleDigest(c.Base) != before {
		t.Fatal("preflight mutated immutable input")
	}
	unprepared, err := Compile(c, m, "consumer", nil)
	if err == nil || unprepared.Files != nil {
		t.Fatalf("public consumer compilation bypassed credential validation: %v", err)
	}
}
