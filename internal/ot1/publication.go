package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"time"
)

const refreshAnnotation = "argocd.argoproj.io/refresh"

// publicationApps is a closed schedule, not a caller supplied selector. Child
// notifications are conditional; the parent is notified once for every publish.
func publicationApps(p Plan, index int) []string {
	if !p.PublicationRefresh || p.Continuation != nil || index <= 0 || index >= len(p.Phases) || p.Phases[index].Revision == p.Phases[index-1].Revision {
		return nil
	}
	names := []string{"platform-control"}
	for _, name := range append([]string{Source}, Targets...) {
		if p.Phases[index-1].Stage.Applications[name] != "" {
			names = append(names, name)
		}
	}
	return names
}

func publicationSpec(p Plan, index int, name string, prior observation.Object) observation.Object {
	for _, a := range p.Phases[index].Applications {
		if a.Name == name {
			return a.Spec
		}
	}
	// Detached owners must compare the new Git projection before orphan delete.
	return observation.Map(prior["spec"])
}

// A pending parent prune-confirm operation may span the publication. Only its
// two adjacent planned revisions are allowed; this never approves pruning.
func publicationObject(p Plan, index int, name string, o, prior observation.Object) error {
	if o == nil || prior == nil || observation.Reference(o) != AppRef(name) ||
		observation.String(observation.At(prior, "metadata", "uid")) == "" ||
		observation.At(o, "metadata", "uid") != observation.At(prior, "metadata", "uid") ||
		observation.String(observation.At(o, "metadata", "resourceVersion")) == "" ||
		observation.At(o, "metadata", "deletionTimestamp") != nil ||
		len(observation.Slice(observation.At(o, "metadata", "finalizers"))) != 0 ||
		len(observation.Slice(observation.At(o, "metadata", "ownerReferences"))) != 0 {
		return errors.New("publication Application identity/deletion fence failed")
	}
	spec := observation.Digest(o["spec"])
	if spec != observation.Digest(prior["spec"]) && spec != observation.Digest(publicationSpec(p, index, name, prior)) {
		return errors.New("publication Application full spec drift")
	}
	for _, path := range [][]string{{"status", "sync", "revision"}, {"operation", "sync", "revision"}} {
		revision := observation.String(observation.At(o, path...))
		if revision != "" && revision != p.Phases[index].Revision && revision != p.Phases[index-1].Revision {
			return errors.New("publication Application unplanned revision")
		}
	}
	if len(observation.Slice(observation.At(o, "status", "conditions"))) != 0 {
		return errors.New("publication Application condition")
	}
	health := observation.String(observation.At(o, "status", "health", "status"))
	if health == "" || health == "Unknown" || health == "Degraded" {
		return errors.New("publication Application health unavailable/degraded")
	}
	phase := observation.String(observation.At(o, "status", "operationState", "phase"))
	if phase == "Running" {
		revision := observation.String(observation.At(o, "status", "operationState", "operation", "sync", "revision"))
		if revision != p.Phases[index].Revision && revision != p.Phases[index-1].Revision {
			return errors.New("publication active operation has unplanned revision")
		}
	}
	if phase == "Failed" || phase == "Error" || phase == "Terminating" {
		return errors.New("publication Application failed operation")
	}
	return nil
}

// Only the refresh annotation is added. Atomic UID/RV/spec tests also protect
// against concurrent changes after observation. No retry after an uncertain
// response and no overwriting another actor's refresh.
func normalRefreshPatch(current observation.Object) ([]observation.Object, error) {
	if observation.At(current, "metadata", "annotations", refreshAnnotation) != nil {
		return nil, errors.New("publication found an existing refresh request")
	}
	patch := []observation.Object{
		{"op": "test", "path": "/metadata/uid", "value": observation.At(current, "metadata", "uid")},
		{"op": "test", "path": "/metadata/resourceVersion", "value": observation.At(current, "metadata", "resourceVersion")},
		{"op": "test", "path": "/spec", "value": current["spec"]},
	}
	if observation.At(current, "metadata", "annotations") == nil {
		patch = append(patch, observation.Object{"op": "add", "path": "/metadata/annotations", "value": observation.Object{refreshAnnotation: "normal"}})
	} else {
		patch = append(patch, observation.Object{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1refresh", "value": "normal"})
	}
	return patch, nil
}

func (x *Executor) publicationMark(index int, event string, o observation.Object) error {
	return observation.CreatePrivate(filepath.Join(x.EvidenceDirectory, fmt.Sprintf("publication-%02d-%s.json", index, event)), observation.Bytes(observation.Object{
		"planSHA256": observation.Digest(x.Plan), "revision": x.Plan.Phases[index].Revision,
		"observedAt": time.Now().UTC(), "application": observation.Reference(o).Name,
		"uid": observation.At(o, "metadata", "uid"), "resourceVersion": observation.At(o, "metadata", "resourceVersion"),
		"specSHA256": observation.Digest(o["spec"]), "reconciledAt": observation.At(o, "status", "reconciledAt"),
		"operationFinishedAt": observation.At(o, "status", "operationState", "finishedAt"),
	}))
}

// Called only inside publish, after the exact remote Git fence. All waits share
// the original stage context. Observer remains read-only, and old plans have no
// notification authority. Notification acceptance is never completion proof.
func (x *Executor) requestNormalRefreshForPublishedRevision(ctx context.Context, index int, previous Snapshot) error {
	names := publicationApps(x.Plan, index)
	if len(names) == 0 {
		return errors.New("plan has no publication notification authority")
	}
	prior := rawIndex(previous.Applications)
	restored := map[string]bool{}
	read := func() (map[string]observation.Object, error) {
		if e := x.fence(ctx, x.Plan.Phases[index].Revision); e != nil {
			return nil, e
		}
		apps := map[string]observation.Object{}
		for _, name := range names {
			o, e := x.app(ctx, name)
			if e != nil {
				return nil, e
			}
			if e = publicationObject(x.Plan, index, name, o, prior[AppRef(name).Key()]); e != nil {
				return nil, fmt.Errorf("%s: %w", name, e)
			}
			if restored[name] && observation.Digest(o["spec"]) != observation.Digest(publicationSpec(x.Plan, index, name, prior[AppRef(name).Key()])) {
				return nil, errors.New("child spec reverted after publication restoration")
			}
			apps[name] = o
		}
		return apps, nil
	}
	notify := func(name string, apps map[string]observation.Object) error {
		patch, e := normalRefreshPatch(apps[name])
		if e != nil {
			return e
		}
		_, e = x.kube(ctx, index, []string{"patch", "application", name, "-n", "argocd", "--type=json", "-p", string(observation.Bytes(patch))}, nil)
		return e
	}
	apps, e := read()
	if e != nil {
		return e
	}
	started := time.Now().UTC().Truncate(time.Second) // Argo timestamps have second precision.
	if e = x.publicationMark(index, "git-confirmed", nil); e != nil {
		return e
	}
	if e = notify("platform-control", apps); e != nil {
		return e
	}
	current := func(o observation.Object) bool {
		compared, err := time.Parse(time.RFC3339, observation.String(observation.At(o, "status", "reconciledAt")))
		return err == nil && !compared.Before(started) &&
			observation.At(o, "metadata", "annotations", refreshAnnotation) == nil &&
			observation.String(observation.At(o, "status", "sync", "revision")) == x.Plan.Phases[index].Revision
	}
	// Do not wait for parent health here: it depends on child health, and a
	// detached parent can legitimately wait for prune confirmation.
	if e = x.wait(ctx, func() (bool, error) {
		apps, e = read()
		return e == nil && current(apps["platform-control"]), e
	}); e != nil {
		return e
	}
	if e = x.publicationMark(index, "parent-compared", apps["platform-control"]); e != nil {
		return e
	}
	for _, name := range names[1:] {
		want := publicationSpec(x.Plan, index, name, prior[AppRef(name).Key()])
		if e = x.wait(ctx, func() (bool, error) {
			apps, e = read()
			return e == nil && observation.Digest(apps[name]["spec"]) == observation.Digest(want), e
		}); e != nil {
			return e
		}
		restored[name] = true
		if e = x.publicationMark(index, name+"-spec", apps[name]); e != nil {
			return e
		}
		if !current(apps[name]) {
			if e = notify(name, apps); e != nil {
				return e
			}
		}
		if e = x.wait(ctx, func() (bool, error) {
			apps, e = read()
			if e != nil {
				return false, e
			}
			o := apps[name]
			if observation.Digest(o["spec"]) != observation.Digest(want) {
				return false, errors.New("child spec reverted after publication restoration")
			}
			if !current(o) || o["operation"] != nil || observation.String(observation.At(o, "status", "operationState", "phase")) == "Running" ||
				observation.String(observation.At(o, "status", "sync", "status")) != "Synced" ||
				observation.String(observation.At(o, "status", "health", "status")) != "Healthy" {
				return false, nil
			}
			if observation.At(o, "status", "operationState") != nil {
				return comparisonAfterOperation(o)
			}
			return true, nil
		}); e != nil {
			return e
		}
		if e = x.publicationMark(index, name+"-compared", apps[name]); e != nil {
			return e
		}
	}
	return nil
}
