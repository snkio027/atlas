package ot1

import (
	"context"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Script complete successful reads, then advance one ordinary leaf at an exact
// API boundary. All four views are actual Capture output, not repaired fixtures.
type captureScript struct {
	lateName                   string
	held                       map[string]string
	onTick                     func(*captureScript)
	plan                       Plan
	snapshot                   Snapshot
	oldRevision                string
	calls, advanceAt, captures int
	events                     []string
	faultAt                    int
	fault                      string
	remote                     string
}

func (r *captureScript) tick(label string) {
	r.calls++
	r.events = append(r.events, label)
	if r.onTick != nil {
		r.onTick(r)
	}
}
func (r *captureScript) ClusterIdentity(context.Context) (string, error) {
	r.tick("cluster")
	if r.calls == r.faultAt && r.fault == "target" {
		return "replacement", nil
	}
	return r.plan.Target.ClusterUID, nil
}
func (r *captureScript) object(o observation.Object) observation.Object {
	out := observation.Clone(o)
	name := observation.Reference(out).Name
	if revision := r.held[name]; revision != "" {
		observation.Map(observation.At(out, "status", "sync"))["revision"] = revision
	}
	leaf := r.lateName
	if leaf == "" {
		leaf = "argocd-self"
	}
	if observation.Reference(out) == AppRef(leaf) && r.advanceAt > 0 {
		if r.calls < r.advanceAt {
			observation.Map(observation.At(out, "status", "sync"))["revision"] = r.oldRevision
		} else {
			delete(r.held, leaf)
			observation.Map(observation.At(out, "status", "sync"))["revision"] = observation.At(o, "status", "sync", "revision")
		}
	}
	if r.calls == r.faultAt && r.fault != "" {
		switch r.fault {
		case "uid":
			observation.Map(out["metadata"])["uid"] = "replacement"
		case "spec":
			out["spec"] = observation.Object{"unexpected": true}
		case "tracking":
			observation.Map(out["metadata"])["annotations"] = observation.Object{observation.TrackingAnnotation: "foreign"}
		case "status":
			observation.Map(out["status"])["unknownFact"] = true
		}
	}
	return out
}
func (r *captureScript) Read(_ context.Context, ref observation.Ref) (observation.Object, error) {
	r.tick("get:" + ref.Key())
	if r.calls == r.faultAt && r.fault == "unavailable" {
		return nil, errors.New("read unavailable")
	}
	o := rawIndex(r.snapshot.Envelope.Raw)[ref.Key()]
	if o == nil {
		return nil, nil
	}
	return r.object(o), nil
}
func (r *captureScript) List(_ context.Context, ref observation.Ref) ([]observation.Object, error) {
	r.tick("list:" + ref.Kind)
	if r.calls == r.faultAt && r.fault == "unavailable" {
		return nil, errors.New("list unavailable")
	}
	var objects []observation.Object
	switch ref.Kind {
	case "Application":
		objects = r.snapshot.Applications
	case "AppProject":
		objects = r.snapshot.Projects
		r.captures++
	case "Node":
		objects = r.snapshot.Nodes
	default:
		return nil, errors.New("unexpected inventory")
	}
	out := []observation.Object{}
	for _, o := range objects {
		out = append(out, r.object(o))
	}
	return out, nil
}
func (r *captureScript) ListApplications(ctx context.Context) ([]observation.Object, error) {
	return r.List(ctx, AppRef(""))
}
func (r *captureScript) Run(_ context.Context, q atlas.Request) ([]byte, error) {
	if q.Tool != "git" || len(q.Args) == 0 || q.Args[0] != "ls-remote" {
		return nil, errors.New("read-only capture attempted write")
	}
	return []byte(r.remote + "\trefs/heads/" + r.plan.Branch + "\n"), nil
}
func captureFixture(t *testing.T, index int) (*Executor, *captureScript, []Snapshot) {
	t.Helper()
	p, d := revisionPlanFixture(t)
	files := p.Revisions["baseline"].FilesSHA256
	for i := range p.Phases {
		name := revisionName(p.Phases[i].Stage.Name)
		if _, ok := p.Revisions[name]; !ok {
			p.Revisions[name] = Revision{Commit: fmt.Sprintf("%040x", len(p.Revisions)+1), FilesSHA256: files}
		}
		p.Phases[i].Revision = p.Revisions[name].Commit
		for j := range p.Phases[i].Applications {
			p.Phases[i].Applications[j].Revision = p.Phases[i].Revision
		}
	}
	dir := privateTemp(t)
	kube := []byte("synthetic capture fixture")
	p.Target.KubeconfigSHA256 = observation.SHA(kube)
	if e := observation.CreatePrivate(filepath.Join(dir, ".state/kubeconfig"), kube); e != nil {
		t.Fatal(e)
	}
	if e := observation.CreatePrivate(filepath.Join(dir, ".state/audit/events"), nil); e != nil {
		t.Fatal(e)
	}
	ss := syntheticSnapshots(t, p, d)
	r := &captureScript{plan: p, snapshot: ss[index], oldRevision: p.BaselineRevision, remote: p.Phases[index].Revision}
	x := &Executor{Plan: p, RuntimeRepository: dir, EvidenceDirectory: dir, Reader: r, Runner: r, Desired: d, baseline: &ss[0], baselineAudit: map[string]bool{}}
	return x, r, ss
}

func TestCaptureRaceAtEveryReadBoundary(t *testing.T) {
	// All six publications and all four Gates. Baseline has no equivalence;
	// its exact comparison is tested separately below.
	for _, index := range []int{1, 12, 13, 23, 24, 28} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				x, r, ss := captureFixture(t, index)
				if _, e := Capture(context.Background(), r, x.Plan, index, &ss[0]); e != nil {
					t.Fatal(e)
				}
				boundaries := r.calls
				events := append([]string(nil), r.events...)
				for boundary := 2; boundary < boundaries; boundary++ {
					// One full API-boundary sweep exercises the shared collector.
					// Other publications cover all four Application views, avoiding
					// a redundant phase x identical resource-read Cartesian product.
					if index != 1 && events[boundary-1] != "list:Application" && events[boundary-1] != "get:"+AppRef("argocd-self").Key() {
						continue
					}
					r.calls, r.captures, r.advanceAt = 0, 0, boundary
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					got, e := x.captureCoherent(ctx, index, &ss[0], &ss[index-1])
					cancel()
					if e != nil {
						t.Fatalf("boundary %d: %v", boundary, e)
					}
					if assessed := Assess(x.Plan, index, got, &ss[0], &ss[index-1], x.Desired, nil); assessed.Ownership != "VERIFIED" {
						t.Fatalf("boundary %d: %+v", boundary, assessed)
					}
					if r.captures > 4 || x.request != 0 {
						t.Fatal("capture replayed action or needed unbounded samples", r.captures, x.request)
					}
				}
			})
		})
	}
}

func TestCaptureRaceNeverHidesUnsafeRead(t *testing.T) {
	for _, fault := range []string{"uid", "spec", "tracking", "status", "unavailable", "target"} {
		t.Run(fault, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				x, r, ss := captureFixture(t, 2)
				_, _ = Capture(context.Background(), r, x.Plan, 2, &ss[0])
				events := append([]string(nil), r.events...)
				for boundary, event := range events {
					if fault == "target" && event != "cluster" {
						continue
					}
					if fault != "target" && !strings.HasPrefix(event, "get:argoproj.io/v1alpha1/Application/argocd/argocd-self") && event != "list:AppProject" && event != "list:Node" {
						continue
					}
					if fault == "status" && !strings.Contains(event, "argocd-self") {
						continue
					}
					r.calls, r.captures, r.advanceAt, r.faultAt, r.fault = 0, 0, 5, boundary+2, fault
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					_, e := x.captureCoherent(ctx, 2, &ss[0], &ss[1])
					cancel()
					if e == nil {
						t.Fatalf("concealed %s at %d %s", fault, boundary+1, event)
					}
					if r.captures > 2 || x.request != 0 {
						t.Fatalf("retried unsafe sample %s at %d: %v", fault, boundary+1, e)
					}
				}
			})
		})
	}
}

func TestCaptureBaselineNotAssignedBeforeVerification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x, r, _ := captureFixture(t, 0)
		x.baseline = nil
		r.faultAt, r.fault = 5, "uid"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := x.Converge(ctx, 0, nil, nil); e == nil {
			t.Fatal("baseline drift accepted")
		}
		if x.baseline != nil {
			t.Fatal("invalid sample became baseline")
		}
	})
}

// Keep the fixture's exact old/new views invalid when mixed. Recollection, not
// normalization of stored evidence, is what permits progress.
func TestCaptureRaceDoesNotRepairEvidence(t *testing.T) {
	x, r, ss := captureFixture(t, 2)
	r.advanceAt = 5
	s, e := Capture(context.Background(), r, x.Plan, 2, &ss[0])
	if e != nil {
		t.Fatal(e)
	}
	if Assess(x.Plan, 2, s, &ss[0], &ss[1], x.Desired, nil).Ownership == "VERIFIED" {
		t.Fatal("mixed evidence accepted")
	}
	before := observation.Digest(s)
	if e = x.captureRace(2, s, &ss[0], &ss[1]); e != nil {
		t.Fatal(e)
	}
	if before != observation.Digest(s) {
		t.Fatal("classifier rewrote evidence")
	}
}

func TestCaptureClosingFailureCannotBeOverwrittenByEarlierRace(t *testing.T) {
	x, r, ss := captureFixture(t, 2)
	r.advanceAt = 5
	_, _ = Capture(context.Background(), r, x.Plan, 2, &ss[0])
	for i, event := range r.events {
		if event == "list:Node" && i > 10 {
			r.calls, r.faultAt, r.fault = 0, i+1, "unavailable"
			s, e := Capture(context.Background(), r, x.Plan, 2, &ss[0])
			if e != nil || s.InventoryError != "INVENTORY_UNAVAILABLE" {
				t.Fatal(e, s.InventoryError)
			}
			if x.captureRace(2, s, &ss[0], &ss[1]) == nil {
				t.Fatal("closing failure became retry")
			}
			return
		}
	}
	t.Fatal("missing closing inventory")
}

func TestCaptureRacesShareOriginalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		x, r, ss := captureFixture(t, 28)
		revisions := []string{}
		for _, phase := range x.Plan.Phases {
			if len(revisions) == 0 || revisions[len(revisions)-1] != phase.Revision {
				revisions = append(revisions, phase.Revision)
			}
		}
		r.onTick = func(r *captureScript) {
			if r.events[len(r.events)-1] == "list:Application" && r.captures%2 == 0 {
				cycle := r.captures / 2
				r.oldRevision = revisions[cycle]
				r.advanceAt = r.calls + 15
				for _, objects := range [][]observation.Object{r.snapshot.Applications, r.snapshot.Envelope.Raw} {
					if app := rawIndex(objects)[AppRef("argocd-self").Key()]; app != nil {
						observation.Map(observation.At(app, "status", "sync"))["revision"] = revisions[cycle+1]
					}
				}
			}
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := x.captureCoherent(ctx, 28, &ss[0], &ss[27])
		if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) != 5*time.Second {
			t.Fatal("deadline reset/early failure", e, time.Since(start))
		}
		if r.captures != 6 || x.request != 0 {
			t.Fatal("expected three discarded complete samples, no writes", r.captures, x.request)
		}
	})
}

func TestCaptureWaitsForF12OnEveryRawView(t *testing.T) {
	for _, index := range []int{3, 4, 6, 7, 10, 11, 15, 16, 18, 19, 21, 22, 26, 27} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				x, r, ss := captureFixture(t, index)
				owner := *x.Plan.Phases[index].Stage.ActiveOwner
				start := time.Now()
				// The opening list is fresh, but the first raw read has not yet
				// observed the completed comparison. SameProof intentionally
				// excludes this timestamp; readiness still must inspect it.
				r.onTick = func(r *captureScript) {
					if r.events[len(r.events)-1] == "get:"+AppRef(owner).Key() {
						app := rawIndex(r.snapshot.Envelope.Raw)[AppRef(owner).Key()]
						at := observation.At(app, "status", "operationState", "finishedAt")
						if time.Since(start) < 4*time.Second {
							at = "2026-09-26T00:00:00Z"
						}
						observation.Map(app["status"])["reconciledAt"] = at
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				got, e := x.captureCoherent(ctx, index, &ss[0], &ss[index-1])
				if e != nil {
					t.Fatal(e)
				}
				if time.Since(start) != 6*time.Second || x.request != 0 {
					t.Fatal("F12 bypass or mutation replay", time.Since(start))
				}
				if Assess(x.Plan, index, got, &ss[0], &ss[index-1], x.Desired, nil).Ownership != "VERIFIED" {
					t.Fatal("fresh complete sample rejected")
				}
			})
		})
	}
}

func TestReadWaitCannotSucceedAfterPhaseDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		x := Executor{}
		if e := x.wait(ctx, func() (bool, error) { time.Sleep(2 * time.Second); return true, nil }); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("accepted late result", e)
		}
	})
}
