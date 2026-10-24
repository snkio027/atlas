package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type publicationScript struct {
	t         *testing.T
	x         *Executor
	index     int
	previous  Snapshot
	old       map[string]observation.Object
	remote    string
	start     time.Time
	requested map[string]time.Time
	count     map[string]int
	stall     string
	drift     string
	driftAt   time.Duration
	events    []string
}

func (s *publicationScript) ClusterIdentity(context.Context) (string, error) {
	if s.drift == "cluster" && time.Since(s.start) >= s.driftAt {
		return "different-cluster", nil
	}
	return s.x.Plan.Target.ClusterUID, nil
}
func (s *publicationScript) List(context.Context, observation.Ref) ([]observation.Object, error) {
	return nil, errors.New("unexpected list")
}
func (s *publicationScript) ListApplications(context.Context) ([]observation.Object, error) {
	return nil, errors.New("unexpected list")
}
func (s *publicationScript) Read(_ context.Context, ref observation.Ref) (observation.Object, error) {
	if ref.Kind != "Application" {
		return observation.Clone(rawIndex(s.previous.Envelope.Raw)[ref.Key()]), nil
	}
	o := observation.Clone(s.old[ref.Key()])
	if o == nil {
		return nil, errors.New("unknown App")
	}
	m, status := observation.Map(o["metadata"]), observation.Map(o["status"])
	m["resourceVersion"] = fmt.Sprint(1 + int(time.Since(s.start)/time.Second))
	annotations := observation.Map(m["annotations"])
	if annotations == nil {
		annotations = observation.Object{}
		m["annotations"] = annotations
	}
	requested, notified := s.requested[ref.Name]
	if notified {
		annotations[refreshAnnotation] = "normal"
	}
	parentTime, parentRequested := s.requested["platform-control"]
	restored := parentRequested && time.Since(parentTime) >= 4*time.Second && s.stall != "spec"
	if ref.Name != "platform-control" && restored {
		o["spec"] = publicationSpec(s.x.Plan, s.index, ref.Name, s.old[ref.Key()])
	}
	if notified && time.Since(requested) >= 2*time.Second && s.stall != "refresh" {
		delete(annotations, refreshAnnotation)
		observation.Map(status["sync"])["revision"] = s.x.Plan.Phases[s.index].Revision
		status["reconciledAt"] = time.Now().UTC().Format(time.RFC3339)
	}
	if s.drift != "" && time.Since(s.start) >= s.driftAt {
		switch s.drift {
		case "uid":
			m["uid"] = "replaced"
		case "spec":
			o["spec"] = observation.Object{"unexpected": true}
		case "regression":
			if ref.Name != "platform-control" && notified {
				o["spec"] = s.old[ref.Key()]["spec"]
			}
		}
	}
	return o, nil
}
func (s *publicationScript) Run(ctx context.Context, q atlas.Request) ([]byte, error) {
	if q.Tool == "git" {
		if len(q.Args) > 0 && q.Args[0] == "ls-remote" {
			remote := s.remote
			if s.drift == "git" && time.Since(s.start) >= s.driftAt {
				remote = strings.Repeat("f", 40)
			}
			return []byte(remote + "\trefs/heads/" + s.x.Plan.Branch + "\n"), nil
		}
		for _, a := range q.Args {
			if a == "push" {
				s.remote = s.x.Plan.Phases[s.index].Revision
				s.events = append(s.events, "push")
			}
		}
		return nil, nil
	}
	if q.Tool != "kubectl" {
		return nil, errors.New("unexpected tool")
	}
	args := q.Args
	i := 0
	for i < len(args) && args[i] != "patch" {
		i++
	}
	if i+3 >= len(args) || args[i+1] != "application" {
		s.t.Fatalf("unbounded request: %v", args)
	}
	name := args[i+2]
	if s.remote != s.x.Plan.Phases[s.index].Revision {
		s.t.Fatal("refresh before exact published Git")
	}
	if s.count[name] != 0 {
		s.t.Fatal("duplicate refresh")
	}
	s.count[name]++
	var patch []observation.Object
	if e := observation.Decode([]byte(args[len(args)-1]), &patch, true); e != nil {
		s.t.Fatal(e)
	}
	current, e := s.Read(ctx, AppRef(name))
	if e != nil {
		s.t.Fatal(e)
	}
	want, e := normalRefreshPatch(current)
	if e != nil || observation.Digest(patch) != observation.Digest(want) {
		s.t.Fatal("patch escaped exact UID/RV/spec/annotation fence")
	}
	if name != "platform-control" && observation.Digest(current["spec"]) != observation.Digest(publicationSpec(s.x.Plan, s.index, name, s.old[AppRef(name).Key()])) {
		s.t.Fatal("child notification before Argo spec restoration")
	}
	s.requested[name] = time.Now()
	s.events = append(s.events, name)
	if s.stall == "response" {
		return nil, errors.New("unknown patch result")
	}
	return []byte("{}"), nil
}

func publicationFixture(t *testing.T, index int) (*Executor, *publicationScript) {
	t.Helper()
	p, desired := syntheticPlan(t)
	p.EvidenceModel = EvidenceModel
	p.PublicationRefresh = true
	rev := 0
	last := ""
	for i := range p.Phases {
		name := revisionName(p.Phases[i].Stage.Name)
		if name != last {
			rev++
			last = name
		}
		sha := strings.Repeat(fmt.Sprint(rev), 40)
		p.Phases[i].Revision = sha
		for j := range p.Phases[i].Applications {
			p.Phases[i].Applications[j].Revision = sha
		}
	}
	snapshots := syntheticSnapshots(t, p, desired)
	previous := snapshots[index-1]
	dir := privateTemp(t)
	if e := os.MkdirAll(filepath.Join(dir, ".state/audit"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, ".state/audit/events"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, ".state/kubeconfig"), []byte("synthetic private fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	p.Target.KubeconfigSHA256 = observation.SHA([]byte("synthetic private fixture"))
	x := &Executor{Plan: p, Repository: dir, RuntimeRepository: dir, EvidenceDirectory: dir, baseline: &snapshots[0], baselineAudit: map[string]bool{}}
	s := &publicationScript{t: t, x: x, index: index, previous: previous, old: rawIndex(previous.Applications), remote: previous.Revision, start: time.Now(), requested: map[string]time.Time{}, count: map[string]int{}}
	// Successful prior operations can be older than the last published revision.
	// They are historical; only an active operation is revision-fenced here.
	for _, o := range s.old {
		status := observation.Map(o["status"])
		status["reconciledAt"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
		delete(status, "operationState")
	}
	x.Reader = s
	x.Runner = s
	return x, s
}

func TestPublicationTemporalParentThenChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x, s := publicationFixture(t, 12)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		if e := x.publish(ctx, 12, s.previous); e != nil {
			t.Fatal(e)
		}
		if strings.Join(s.events, ",") != "push,platform-control,"+Source {
			t.Fatal(s.events)
		}
		if time.Since(s.start) != 6*time.Second {
			t.Fatalf("did not wait for parent, restored spec and child comparison: %v", time.Since(s.start))
		}
		for _, name := range []string{"parent-compared", Source + "-spec", Source + "-compared"} {
			if _, e := os.Stat(filepath.Join(x.EvidenceDirectory, "publication-12-"+name+".json")); e != nil {
				t.Fatal(e)
			}
		}
	})
}

func TestPublicationMissingLateAndDriftingStatesStop(t *testing.T) {
	for _, tc := range []struct {
		name, stall, drift string
		at                 time.Duration
		writes             int
	}{
		{"refresh-not-consumed", "refresh", "", 0, 1},
		{"spec-never-restored", "spec", "", 0, 1},
		{"uncertain-response", "response", "", 0, 1},
		{"Git-changed", "", "git", 2 * time.Second, 1},
		{"cluster-changed", "", "cluster", 2 * time.Second, 1},
		{"UID-changed", "", "uid", 2 * time.Second, 1},
		{"spec-changed", "", "spec", 2 * time.Second, 1},
		{"spec-regressed", "", "regression", 6 * time.Second, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				x, s := publicationFixture(t, 12)
				s.stall = tc.stall
				s.drift = tc.drift
				s.driftAt = tc.at
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if e := x.publish(ctx, 12, s.previous); e == nil {
					t.Fatal("accepted incomplete or drifted publication")
				}
				n := 0
				for _, v := range s.count {
					n += v
				}
				if n != tc.writes {
					t.Fatalf("writes=%d expected=%d", n, tc.writes)
				}
				if time.Since(s.start) > 10*time.Second {
					t.Fatal("escaped shared budget")
				}
				if _, e := os.Stat(filepath.Join(x.EvidenceDirectory, "publication-12-"+Source+"-compared.json")); !os.IsNotExist(e) {
					t.Fatal("completion evidence despite STOP")
				}
			})
		})
	}
}

func TestPublicationAllSixSchedulesAreBounded(t *testing.T) {
	for _, index := range []int{1, 12, 13, 23, 24, 28} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				x, s := publicationFixture(t, index)
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
				defer cancel()
				if e := x.publish(ctx, index, s.previous); e != nil {
					t.Fatal(e)
				}
				allowed := publicationApps(x.Plan, index)
				if len(s.count) != len(allowed) {
					t.Fatalf("%v != %v", s.count, allowed)
				}
				for _, name := range allowed {
					if s.count[name] != 1 {
						t.Fatal("missing or repeated refresh")
					}
				}
				x.Plan.PublicationRefresh = false
				if publicationApps(x.Plan, index) != nil {
					t.Fatal("historical plan gained authority")
				}
				x.Plan.PublicationRefresh = true
				x.Plan.Continuation = &ContinuationBinding{}
				if publicationApps(x.Plan, index) != nil {
					t.Fatal("continuation gained authority")
				}
			})
		})
	}
}

func TestNormalRefreshPatchPreservesAnnotations(t *testing.T) {
	o := live(observation.Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": observation.Object{"name": Source, "namespace": "argocd", "annotations": observation.Object{observation.TrackingAnnotation: "preserve"}}, "spec": observation.Object{"source": "preserve"}}, "uid")
	patch, e := normalRefreshPatch(o)
	if e != nil || len(patch) != 4 || patch[3]["path"] != "/metadata/annotations/argocd.argoproj.io~1refresh" || patch[3]["value"] != "normal" {
		t.Fatal(patch, e)
	}
	observation.Map(observation.At(o, "metadata", "annotations"))[refreshAnnotation] = "hard"
	if _, e = normalRefreshPatch(o); e == nil {
		t.Fatal("overwrote existing request")
	}
}

// A publication can advance an ordinary leaf while a multi-object capture is
// open. Both stable revisions may already satisfy Desired Identity; combining
// their observations must still fail the existing coherence proof. Any future
// bounded recapture must discard this sample rather than turn it into PASS.
func TestPublicationLeafAdvanceRequiresFreshCoherentCapture(t *testing.T) {
	p, desired, snapshots := revisionFixture(t)
	copySnapshot := func(in Snapshot) Snapshot {
		var out Snapshot
		if e := observation.Decode(observation.Bytes(in), &out, true); e != nil {
			t.Fatal(e)
		}
		return out
	}
	before := copySnapshot(snapshots[1])
	for _, objects := range [][]observation.Object{before.Applications, before.Envelope.Raw} {
		for _, o := range objects {
			if observation.Reference(o) == AppRef("argocd-self") {
				observation.Map(observation.At(o, "status", "sync"))["revision"] = p.BaselineRevision
			}
		}
	}
	before = proofSnapshot(before)
	after := copySnapshot(snapshots[1])
	for _, sample := range []Snapshot{before, after} {
		ready, e := applicationProgress(p, 1, sample.Applications, &snapshots[0], "")
		if e != nil || !ready {
			t.Fatal("stable eligible sample rejected", ready, e)
		}
		if got := Assess(p, 1, sample, &snapshots[0], &snapshots[0], desired, nil); got.Ownership != "VERIFIED" {
			t.Fatal(got)
		}
	}
	mixed := copySnapshot(before)
	mixed.ClosingApplications = after.ClosingApplications
	mixed.Envelope.ClosingRaw = after.Envelope.ClosingRaw
	if got := Assess(p, 1, mixed, &snapshots[0], &snapshots[0], desired, nil); got.Ownership != "STOP" {
		t.Fatal("an incoherent sample was accepted", got)
	}
}
