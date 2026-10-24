package ot1

import (
	"context"
	"errors"
	"github.com/snkio027/atlas/internal/observation"
	"path"
	"strings"
)

const EvidenceModel = "ot1.semantic-transitions/v1"

func sourceDigest(p Plan, revision string, spec observation.Object) string {
	source := observation.Map(spec["source"])
	dir := observation.String(source["path"])
	if len(source) != 3 || spec["sources"] != nil || source["repoURL"] != p.Repository || source["targetRevision"] != p.Branch || !strings.HasPrefix(dir, "gitops/") || path.Clean(dir) != dir {
		return ""
	}
	var files map[string]string
	for _, r := range p.Revisions {
		if r.Commit == revision {
			files = r.FilesSHA256
		}
	}
	inputs := map[string]string{}
	for file, hash := range files {
		if strings.HasPrefix(file, dir+"/") {
			if !observation.Hash(hash) {
				return ""
			}
			inputs[file] = hash
		}
	}
	if len(inputs) < 2 || inputs[dir+"/kustomization.yaml"] == "" {
		return ""
	}
	return observation.Digest(inputs)
}

// validateSourceClosures deliberately supports only the reviewed generated local
// Kustomizations. Bases, generators, remote inputs, plugins and multi-source Apps
// require another model, not an optimistic subtree comparison.
func validateSourceClosures(ctx context.Context, repo string, p Plan) error {
	seen := map[string]bool{}
	for _, phase := range p.Phases {
		for _, app := range phase.Applications {
			if destination(app.Name) != "" || app.Name == "platform-control" {
				continue
			}
			dir := observation.String(observation.At(app.Spec, "source", "path"))
			key := phase.Revision + ":" + dir
			if seen[key] {
				continue
			}
			seen[key] = true
			if sourceDigest(p, phase.Revision, app.Spec) == "" {
				return errors.New("source equivalence inputs missing")
			}
			b, e := sourceFile(ctx, repo, phase.Revision, dir+"/kustomization.yaml")
			if e != nil {
				return e
			}
			var k struct {
				APIVersion string   `json:"apiVersion"`
				Kind       string   `json:"kind"`
				Resources  []string `json:"resources"`
			}
			if observation.Decode(b, &k, true) != nil || k.APIVersion != "kustomize.config.k8s.io/v1beta1" || k.Kind != "Kustomization" || len(k.Resources) == 0 {
				return errors.New("source closure is not a supported local render")
			}
			files := p.Revisions[revisionName(phase.Stage.Name)].FilesSHA256
			names := map[string]bool{}
			for _, name := range k.Resources {
				if name == "." || name == ".." || path.Base(name) != name || strings.Contains(name, ":") || names[name] || !observation.Hash(files[dir+"/"+name]) {
					return errors.New("source closure escapes locked local files")
				}
				names[name] = true
			}
		}
	}
	return nil
}

// desiredEquivalent is read-only evidence, never mutation authorization. Walk
// only the already-published prefix of this immutable plan; a content/spec
// change (including change-and-revert) terminates the equivalence class.
func desiredEquivalent(p Plan, index int, want observation.ExpectedApplication, app, prior observation.Object) bool {
	if p.EvidenceModel != EvidenceModel || index <= 0 || index >= len(p.Phases) || destination(want.Name) != "" || want.Name == "platform-control" || prior == nil {
		return false
	}
	if active := p.Phases[index].Stage.ActiveOwner; active != nil && *active == want.Name {
		return false
	}
	observed := observation.String(observation.At(app, "status", "sync", "revision"))
	if !observation.FullSHA(observed) || observation.String(observation.At(prior, "metadata", "uid")) == "" || observation.At(app, "metadata", "uid") != observation.At(prior, "metadata", "uid") || observation.Digest(prior["spec"]) != observation.Digest(want.Spec) || observation.Digest(app["spec"]) != observation.Digest(want.Spec) {
		return false
	}
	current := sourceDigest(p, p.Phases[index].Revision, want.Spec)
	if current == "" {
		return false
	}
	for i := index; i >= 0; i-- {
		phase := p.Phases[i]
		found := false
		for _, planned := range phase.Applications {
			if planned.Name == want.Name {
				found = observation.Digest(planned.Spec) == observation.Digest(want.Spec)
				break
			}
		}
		if !found || sourceDigest(p, phase.Revision, want.Spec) != current {
			return false
		}
		if phase.Revision == observed {
			return true
		}
	}
	return false
}

// Only the derived expectation changes; raw facts and the phase SHA stay exact.
func transitionExpectation(p Plan, index int, want observation.ExpectedApplication, app, prior observation.Object) observation.ExpectedApplication {
	if desiredEquivalent(p, index, want, app, prior) {
		want.Revision = observation.String(observation.At(app, "status", "sync", "revision"))
	}
	return want
}
