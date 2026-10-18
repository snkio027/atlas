package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Compare resolved closures, not top-level spelling: selecting a dependent may
// subsume a formerly explicit selection. Include the last local projection so
// editing enabled.json then running render cannot silently remove Applications.
// This is a worktree guard, not a live inventory or server-side retirement lock.
func (p *Project) removedCapabilities(names []string) ([]string, error) {
	current := map[string]bool{}
	for _, name := range p.Capabilities.Active {
		current[name] = true
	}
	coreBytes, e := readInput(filepath.Join(p.Root, capabilityDir, "core-applications.json"))
	if e != nil {
		return nil, e
	}
	core, e := decodeObjects(coreBytes)
	if e != nil {
		return nil, e
	}
	coreNames := map[string]bool{}
	for _, o := range core {
		coreNames[str(metadata(o)["name"])] = true
	}
	b, e := readInput(filepath.Join(p.Root, platformApplications))
	if e != nil {
		return nil, e
	}
	if e = uniqueJSONKeys(b); e != nil {
		return nil, e
	}
	previous, e := decodeObjects(b)
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, o := range previous {
		name := str(metadata(o)["name"])
		if o["apiVersion"] != "argoproj.io/v1alpha1" || o["kind"] != "Application" || name == "" || metadata(o)["namespace"] != p.namespace("argocd") || seen[name] {
			return nil, errors.New("invalid previous Application projection")
		}
		seen[name] = true
		if !coreNames[name] {
			current[name] = true
		}
	}
	for name := range coreNames {
		if !seen[name] {
			return nil, fmt.Errorf("previous projection lacks core Application %s; refusing to assume an empty baseline", name)
		}
	}
	for _, name := range names {
		delete(current, name)
	}
	removed := []string{}
	for name := range current {
		removed = append(removed, name)
	}
	sort.Strings(removed)
	return removed, nil
}

func (p *Project) ValidateCapabilitySelection(names []string) error {
	removed, e := p.removedCapabilities(names)
	if e != nil {
		return e
	}
	if len(removed) > 0 {
		return fmt.Errorf("capability selection is enable-only; retirement/ownership migration is unsupported: %s; use a separately reviewed migration contract", strings.Join(removed, ", "))
	}
	return nil
}
