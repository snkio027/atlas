package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A scripted controller for the existing real Executor, not another executor.
// Writes are interpreted only for the fixed ceremony objects; all evidence is
// produced by real Capture/Assess, and the real runner records every request.
type ceremonyScript struct {
	*captureScript
	t         *testing.T
	x         *Executor
	snapshots []Snapshot
	index     int
	writes    []string
	refresh   map[string]time.Time
	failWrite int
}

func (s *ceremonyScript) setApp(app observation.Object) {
	key := observation.Reference(app).Key()
	found := false
	for i, o := range s.snapshot.Applications {
		if observation.Reference(o).Key() == key {
			s.snapshot.Applications[i] = app
			found = true
		}
	}
	if !found {
		s.snapshot.Applications = append(s.snapshot.Applications, app)
	}
	found = false
	for i, o := range s.snapshot.Envelope.Raw {
		if observation.Reference(o).Key() == key {
			s.snapshot.Envelope.Raw[i] = app
			found = true
		}
	}
	if !found {
		s.snapshot.Envelope.Raw = append(s.snapshot.Envelope.Raw, app)
	}
}
func (s *ceremonyScript) Read(ctx context.Context, ref observation.Ref) (observation.Object, error) {
	if start, ok := s.refresh[ref.Name]; ok && time.Since(start) >= 2*time.Second {
		app := rawIndex(s.snapshot.Applications)[ref.Key()]
		delete(observation.Map(observation.At(app, "metadata", "annotations")), refreshAnnotation)
		observation.Map(app["status"])["reconciledAt"] = time.Now().UTC().Format(time.RFC3339)
		observation.Map(observation.At(app, "status", "sync"))["revision"] = s.remote
		delete(s.refresh, ref.Name)
	}
	return s.captureScript.Read(ctx, ref)
}
func (s *ceremonyScript) Run(ctx context.Context, q atlas.Request) ([]byte, error) {
	if q.Tool == "git" {
		if len(q.Args) > 0 && q.Args[0] == "ls-remote" {
			return s.captureScript.Run(ctx, q)
		}
		for _, a := range q.Args {
			if a == "push" {
				s.writes = append(s.writes, "publish:"+fmt.Sprint(s.index))
				s.remote = s.plan.Phases[s.index].Revision
				for _, app := range s.snapshot.Applications {
					name := observation.Reference(app).Name
					if destination(name) != "" || name == "platform-control" {
						app["spec"] = publicationSpec(s.plan, s.index, name, app)
						if s.plan.Phases[s.index].Stage.AtlasGate && destination(name) != "" {
							if observation.Map(observation.At(app, "metadata", "annotations")) == nil {
								observation.Map(app["metadata"])["annotations"] = observation.Object{}
							}
							observation.Map(observation.At(app, "metadata", "annotations"))[observation.TrackingAnnotation] = observation.Tracking("platform-control", "argocd", AppRef(name))
						}
					}
				}
			}
		}
		if len(s.writes) == s.failWrite {
			return nil, errors.New("lost Git response")
		}
		return nil, nil
	}
	args := q.Args
	i := 0
	for i < len(args) && args[i] != "create" && args[i] != "delete" && args[i] != "patch" {
		i++
	}
	if i == len(args) {
		return nil, errors.New("unexpected non-ceremony tool")
	}
	verb := args[i]
	name := ""
	switch verb {
	case "create":
		var definition observation.Object
		if e := observation.Decode(q.Input, &definition, false); e != nil {
			return nil, e
		}
		name = observation.Reference(definition).Name
		app := observation.Clone(rawIndex(s.snapshots[s.index].Applications)[AppRef(name).Key()])
		delete(observation.Map(app["status"]), "operationState")
		delete(observation.Map(app["status"]), "conditions")
		observation.Map(observation.At(app, "status", "sync"))["status"] = "Synced"
		s.setApp(app)
		s.writes = append(s.writes, "create:"+name)
		if len(s.writes) == s.failWrite {
			return nil, errors.New("lost write response")
		}
		return observation.Bytes(app), nil
	case "delete":
		name = filepath.Base(strings.TrimPrefix(args[i+1], "--raw="))
		filter := func(objects []observation.Object) []observation.Object {
			out := []observation.Object{}
			for _, o := range objects {
				if observation.Reference(o) != AppRef(name) {
					out = append(out, o)
				}
			}
			return out
		}
		s.snapshot.Applications = filter(s.snapshot.Applications)
		s.snapshot.Envelope.Raw = filter(s.snapshot.Envelope.Raw)
		s.writes = append(s.writes, "delete:"+name)
	case "patch":
		name = args[i+2]
		var patch []observation.Object
		if e := observation.Decode([]byte(args[len(args)-1]), &patch, true); e != nil {
			return nil, e
		}
		app := rawIndex(s.snapshot.Applications)[AppRef(name).Key()]
		for _, item := range patch {
			path := observation.String(item["path"])
			if item["op"] == "test" {
				var actual any
				switch path {
				case "/metadata/uid":
					actual = observation.At(app, "metadata", "uid")
				case "/metadata/resourceVersion":
					actual = observation.At(app, "metadata", "resourceVersion")
				case "/spec":
					actual = app["spec"]
				default:
					return nil, fmt.Errorf("unexpected test path %s", path)
				}
				if observation.Digest(actual) != observation.Digest(item["value"]) {
					return nil, errors.New("atomic test failed")
				}
				continue
			}
			switch {
			case path == "/operation":
				s.writes = append(s.writes, "sync:"+name)
				final := s.snapshots[s.index]
				for j, o := range s.snapshot.Envelope.Raw {
					if observation.Reference(o).Kind != "Application" {
						s.snapshot.Envelope.Raw[j] = observation.Clone(rawIndex(final.Envelope.Raw)[observation.Reference(o).Key()])
					}
				}
				s.setApp(observation.Clone(rawIndex(final.Applications)[AppRef(name).Key()]))
			case path == "/spec/syncPolicy/syncOptions":
				observation.Map(observation.At(app, "spec", "syncPolicy"))["syncOptions"] = item["value"]
				s.writes = append(s.writes, "mode:"+name)
			case strings.HasPrefix(path, "/metadata/annotations"):
				if observation.Map(observation.At(app, "metadata", "annotations")) == nil {
					observation.Map(app["metadata"])["annotations"] = observation.Object{}
				}
				observation.Map(observation.At(app, "metadata", "annotations"))[refreshAnnotation] = "normal"
				s.refresh[name] = time.Now()
				s.writes = append(s.writes, "refresh:"+name)
			default:
				return nil, fmt.Errorf("unexpected patch path %s", path)
			}
		}
	}
	if len(s.writes) == s.failWrite {
		return nil, errors.New("lost write response")
	}
	return []byte("{}"), nil
}

type temporalDriver struct {
	s     *ceremonyScript
	races bool
	gates map[int]int
}

func (d *temporalDriver) Transition(ctx context.Context, index int, previous *Snapshot) error {
	d.s.index = index
	if d.races && index == 2 {
		d.s.lateName = "project-bootstrap"
		d.s.oldRevision = d.s.plan.BaselineRevision
		d.s.advanceAt = d.s.calls + 20
	}
	return d.s.x.Transition(ctx, index, previous)
}
func (d *temporalDriver) Converge(ctx context.Context, index int, baseline, previous *Snapshot) (Snapshot, error) {
	s := d.s
	// Controller progress after the one submitted transition. Persistent ordinary
	// leaves may still compare an older, content-equivalent planned revision.
	if index > 0 {
		for _, app := range s.snapshot.Applications {
			if destination(observation.Reference(app).Name) == "" {
				observation.Map(observation.At(app, "status", "sync"))["revision"] = s.plan.Phases[index].Revision
			}
		}
	}
	s.advanceAt = 0
	s.lateName = "argocd-self"
	if d.races && index > 0 && Steps(s.plan)[index].PublishRevision != "" {
		s.oldRevision = s.plan.Phases[index-1].Revision
		s.advanceAt = s.calls + 15
	}
	if d.races && index == 1 {
		s.held["project-bootstrap"] = s.plan.BaselineRevision
	}
	if d.races && index > 0 && s.plan.Phases[index].Stage.AtlasGate {
		s.held["atlas-refactor-root"] = s.plan.Phases[index-1].Revision
	}
	return s.x.Converge(ctx, index, baseline, previous)
}
func (d *temporalDriver) AtlasGate(ctx context.Context, index int, snapshot Snapshot, baseline *Snapshot, dir string) (Snapshot, *GateProof, error) {
	d.gates[index]++
	if d.races && index > 0 {
		d.s.lateName = "atlas-refactor-root"
		d.s.oldRevision = d.s.plan.Phases[index-1].Revision
		d.s.advanceAt = d.s.calls + 20
	}
	// Inputs stand in for the existing Bootstrap and HTTPS/PVC runtime checks.
	// The production closeAtlasGate is used, and cannot call them a second time.
	artifacts := map[string]observation.Object{
		"status.json":       {"state": "ADOPTED"},
		"repeat-apply.json": {"exitCode": 0, "deniedWrites": 0, "beforeIdentitySHA256": IdentityDigest(snapshot), "requests": []any{observation.Object{"tool": "git", "args": []any{"status", "--porcelain"}, "exitCode": 0}}},
		"audit-before.json": {"complete": true, "rawSHA256": strings.Repeat("a", 64), "kubectlMutationAuditIDs": []any{}},
		"audit-after.json":  {"complete": true, "rawSHA256": strings.Repeat("a", 64), "kubectlMutationAuditIDs": []any{}},
		"runtime.json":      runtimeFixture(snapshot),
	}
	return d.s.x.closeAtlasGate(ctx, index, snapshot, baseline, dir, artifacts)
}
func TestTemporalRunKeepsExactWriteSequenceAfterRecapture(t *testing.T) {
	var sequences [][]string
	for _, tc := range []struct {
		races     bool
		failWrite int
	}{{false, -1}, {true, -1}, {true, 1}, {true, 2}, {true, 4}, {true, 5}, {true, 6}, {true, 7}} {
		t.Run(fmt.Sprintf("races=%v/fail=%d", tc.races, tc.failWrite), func(t *testing.T) {
			if tc.failWrite > 0 {
				t.Parallel()
			}
			synctest.Test(t, func(t *testing.T) {
				x, r, ss := captureFixture(t, 0)
				x.Plan.PublicationRefresh = true
				for i := range ss {
					ss[i].PlanSHA256 = observation.Digest(x.Plan)
				}
				for _, snapshot := range ss {
					for _, app := range snapshot.Applications {
						if op := observation.Map(observation.At(app, "status", "operationState")); op != nil {
							op["finishedAt"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
							observation.Map(app["status"])["reconciledAt"] = op["finishedAt"]
						}
					}
				}
				r.plan = x.Plan
				r.held = map[string]string{}
				r.snapshot = cloneSnapshot(t, ss[0])
				x.baseline = nil
				s := &ceremonyScript{captureScript: r, t: t, x: x, snapshots: ss, refresh: map[string]time.Time{}, failWrite: tc.failWrite}
				for _, app := range s.snapshot.Applications {
					s.setApp(app)
				}
				x.Reader = s
				x.Runner = s
				d := &temporalDriver{s: s, races: tc.races, gates: map[int]int{}}
				dir := filepath.Join(privateTemp(t), "attempt")
				x.EvidenceDirectory = dir
				e := Run(context.Background(), x.Plan, observation.Digest(x.Plan), dir, x.Desired, d)
				if tc.failWrite > 0 {
					if e == nil || len(s.writes) != tc.failWrite || !reflect.DeepEqual(s.writes, sequences[0][:tc.failWrite]) {
						t.Fatal("uncertain write was repeated or skipped", e, s.writes)
					}
					return
				}
				if e != nil {
					t.Fatalf("stage %d: %v; writes=%v", s.index, e, s.writes)
				}
				for _, i := range []int{0, 12, 23, 28} {
					if d.gates[i] != 1 {
						t.Fatal("Gate inputs replayed", d.gates)
					}
				}
				want := []string{}
				for i, step := range Steps(x.Plan) {
					if step.PublishRevision != "" {
						want = append(want, "publish:"+fmt.Sprint(i))
						for _, name := range publicationApps(x.Plan, i) {
							want = append(want, "refresh:"+name)
						}
					}
					for _, name := range step.Release {
						want = append(want, "delete:"+name)
					}
					if step.Owner != "" {
						action := "mode:"
						if step.Create {
							action = "create:"
						}
						want = append(want, action+step.Owner, "sync:"+step.Owner)
					}
				}
				if !reflect.DeepEqual(s.writes, want) {
					t.Fatalf("writes differ from planned actions\ngot %v\nwant %v", s.writes, want)
				}
				sequences = append(sequences, s.writes)
			})
		})
	}
	if len(sequences) != 2 || !reflect.DeepEqual(sequences[0], sequences[1]) {
		t.Fatal("read recapture changed writes")
	}
}
