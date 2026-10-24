package workload

import (
	"bytes"
	"fmt"
	"github.com/snkio027/atlas/internal/platform"
	"strings"
)

// Check only generated deltas, never rewrite the frozen D1 tree. Schemas must
// agree with the CRDs in that tree: a newer source checkout cannot silently
// supply defaulting rules for an older installed product.
func validateDefaultStability(c CompileContext, files Files, before, after []OwnedResource) error {
	load := func(files Files) func(OwnedResource) (Object, error) {
		cache := map[string]map[string]Object{}
		return func(r OwnedResource) (Object, error) {
			if cache[r.Path] == nil {
				xs, err := objects(files[r.Path])
				if err != nil {
					return nil, err
				}
				cache[r.Path] = map[string]Object{}
				for _, o := range xs {
					cache[r.Path][identity(o)] = o
				}
			}
			o := cache[r.Path][r.Identity]
			if o == nil {
				return nil, fmt.Errorf("missing default-stability resource %s", r.Identity)
			}
			return o, nil
		}
	}
	oldObject, newObject := load(c.Base), load(files)
	baseline := map[string]OwnedResource{}
	definitions := map[platform.GVK]platform.ResourceType{}
	for _, r := range before {
		baseline[r.Identity] = r
		if !strings.HasPrefix(r.Identity, "apiextensions.k8s.io/CustomResourceDefinition/") {
			continue
		}
		o, err := oldObject(r)
		if err != nil {
			return err
		}
		for _, raw := range arr(val(o, "spec", "versions")) {
			v := obj(raw)
			if v["served"] != true {
				continue
			}
			key := platform.GVK{APIVersion: str(val(o, "spec", "group")) + "/" + str(v["name"]), Kind: str(val(o, "spec", "names", "kind"))}
			if _, exists := definitions[key]; exists {
				return fmt.Errorf("duplicate product CRD schema %v", key)
			}
			definitions[key] = platform.ResourceType{Scope: platform.ResourceScope(str(val(o, "spec", "scope"))), Schema: obj(val(v, "schema", "openAPIV3Schema"))}
		}
	}
	for _, r := range after {
		previous, exists := baseline[r.Identity]
		if exists && previous.SHA256 == r.SHA256 {
			continue
		}
		next, err := newObject(r)
		if err != nil {
			return err
		}
		typ, err := c.ResourceModel.Resolve(next)
		if err != nil {
			return err
		}
		// Built-in scope registry is not an admission schema.
		if typ.Schema == nil {
			continue
		}
		definition, present := definitions[platform.GVK{APIVersion: str(next["apiVersion"]), Kind: str(next["kind"])}]
		if !present || definition.Scope != typ.Scope || !bytes.Equal(JSON(definition.Schema), JSON(typ.Schema)) {
			return fmt.Errorf("%s: CRD schema differs from immutable product base", r.Identity)
		}
		var old Object
		if exists {
			old, err = oldObject(previous)
			if err != nil {
				return err
			}
		}
		if err = c.ResourceModel.ValidateCRDDefaultStability(old, next); err != nil {
			return err
		}
	}
	return nil
}
