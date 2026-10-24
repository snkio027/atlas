package ot1

import (
	"context"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func proofSnapshot(s Snapshot) Snapshot {
	s.Envelope.ProofVersion = observation.ProofVersion
	clone := func(in []observation.Object) []observation.Object {
		out := []observation.Object{}
		for _, o := range in {
			out = append(out, observation.Clone(o))
		}
		return out
	}
	s.Envelope.ClosingRaw = clone(s.Envelope.Raw)
	s.ClosingApplications = clone(s.Applications)
	s.ClosingProjects = clone(s.Projects)
	s.ClosingNodes = clone(s.Nodes)
	return s
}
func revisionPlanFixture(t *testing.T) (Plan, map[string]observation.Object) {
	p, d := syntheticPlan(t)
	p.EvidenceModel = EvidenceModel
	base := strings.Repeat("e", 40)
	p.BaselineRevision = base
	p.Phases[0].Revision = base
	dir := "gitops/test/leaf"
	hashes := map[string]string{dir + "/kustomization.yaml": strings.Repeat("1", 64), dir + "/rendered.yaml": strings.Repeat("2", 64)}
	p.Revisions = map[string]Revision{"baseline": {Commit: base, FilesSHA256: hashes}, "detached-mixed": {Commit: p.Phases[1].Revision, FilesSHA256: hashes}}
	for i := range p.Phases {
		for j := range p.Phases[i].Applications {
			a := &p.Phases[i].Applications[j]
			a.Revision = p.Phases[i].Revision
			if destination(a.Name) == "" {
				a.Spec = observation.Object{"source": observation.Object{"path": dir, "repoURL": p.Repository, "targetRevision": p.Branch}, "project": "platform-project"}
			}
		}
	}
	return p, d
}
func revisionFixture(t *testing.T) (Plan, map[string]observation.Object, []Snapshot) {
	p, d := revisionPlanFixture(t)
	snapshots := syntheticSnapshots(t, p, d)
	for i := range snapshots {
		snapshots[i] = proofSnapshot(snapshots[i])
	}
	return p, d, snapshots
}
func TestTransitionalRevisionEquivalence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan, Snapshot)
		want   bool
	}{
		{"unchanged prior source", func(_ *Plan, _ Snapshot) {}, true},
		{"changed source", func(p *Plan, _ Snapshot) {
			v := p.Revisions["detached-mixed"]
			v.FilesSHA256 = map[string]string{"gitops/test/leaf/kustomization.yaml": strings.Repeat("1", 64), "gitops/test/leaf/rendered.yaml": strings.Repeat("3", 64)}
			p.Revisions["detached-mixed"] = v
		}, false},
		{"unrecognized older revision", func(_ *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "sync"))["revision"] = strings.Repeat("a", 40)
		}, false},
		{"new uid", func(_ *Plan, s Snapshot) {
			observation.Map(rawIndex(s.Applications)[AppRef("argocd-self").Key()]["metadata"])["uid"] = "new"
		}, false},
		{"unhealthy", func(_ *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "health"))["status"] = "Degraded"
		}, false},
		{"platform-control stays exact", func(p *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("platform-control").Key()], "status", "sync"))["revision"] = p.BaselineRevision
		}, false},
		{"Atlas Gate uses the same desired identity", func(p *Plan, _ Snapshot) { p.Phases[1].Stage.AtlasGate = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d, ss := revisionFixture(t)
			s := ss[1]
			app := rawIndex(s.Applications)[AppRef("argocd-self").Key()]
			observation.Map(observation.At(app, "status", "sync"))["revision"] = p.BaselineRevision
			tc.mutate(&p, s)
			s.PlanSHA256 = observation.Digest(p)
			s = proofSnapshot(s)
			got := Assess(p, 1, s, &ss[0], &ss[0], d, nil)
			if (got.Ownership == "VERIFIED") != tc.want {
				t.Fatal(got)
			}
			ready, e := applicationProgress(p, 1, s.Applications, &ss[0], "")
			if tc.want && (!ready || e != nil) {
				t.Fatal(ready, e)
			}
			if !tc.want && ready && e == nil {
				t.Fatal("progress incorrectly ready")
			}
		})
	}
}

func TestFullGateDesiredIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan, Snapshot)
		want   bool
	}{
		{"unchanged previous source", func(*Plan, Snapshot) {}, true},
		{"changed source", func(p *Plan, _ Snapshot) {
			v := p.Revisions["mixed-restored"]
			v.FilesSHA256 = map[string]string{"gitops/test/leaf/kustomization.yaml": strings.Repeat("1", 64), "gitops/test/leaf/rendered.yaml": strings.Repeat("3", 64)}
			p.Revisions["mixed-restored"] = v
		}, false},
		{"unknown revision", func(_ *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "sync"))["revision"] = strings.Repeat("a", 40)
		}, false},
		{"two epochs old with unchanged closure", func(p *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "sync"))["revision"] = p.BaselineRevision
		}, true},
		{"UID changed", func(_ *Plan, s Snapshot) {
			observation.Map(rawIndex(s.Applications)[AppRef("argocd-self").Key()]["metadata"])["uid"] = "replacement"
		}, false},
		{"spec changed", func(_ *Plan, s Snapshot) {
			observation.Map(rawIndex(s.Applications)[AppRef("argocd-self").Key()]["spec"])["project"] = "foreign"
		}, false},
		{"unhealthy", func(_ *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "health"))["status"] = "Degraded"
		}, false},
		{"active operation", func(_ *Plan, s Snapshot) {
			rawIndex(s.Applications)[AppRef("argocd-self").Key()]["operation"] = observation.Object{"sync": observation.Object{}}
		}, false},
		{"error condition", func(_ *Plan, s Snapshot) {
			observation.Map(rawIndex(s.Applications)[AppRef("argocd-self").Key()]["status"])["conditions"] = []any{observation.Object{"type": "ComparisonError"}}
		}, false},
		{"platform-control stays exact", func(p *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("platform-control").Key()], "status", "sync"))["revision"] = p.Phases[11].Revision
		}, false},
		{"foundation owner stays exact", func(p *Plan, s Snapshot) {
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef(Source).Key()], "status", "sync"))["revision"] = p.Phases[11].Revision
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d, _ := revisionFixture(t)
			const index = 12
			p.Phases[index].Revision = strings.Repeat("f", 40)
			for j := range p.Phases[index].Applications {
				p.Phases[index].Applications[j].Revision = p.Phases[index].Revision
			}
			p.Revisions["mixed-restored"] = Revision{Commit: p.Phases[index].Revision, FilesSHA256: p.Revisions["detached-mixed"].FilesSHA256}
			ss := syntheticSnapshots(t, p, d)
			s := ss[index]
			observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "sync"))["revision"] = p.Phases[index-1].Revision
			tc.mutate(&p, s)
			s.PlanSHA256 = observation.Digest(p)
			s = proofSnapshot(s)
			gate := writeGateFixture(t, privateTemp(t), "full-gate", p, p.Phases[index], s)
			got := Assess(p, index, s, &ss[0], &ss[index-1], d, gate)
			if got.Passed() != tc.want {
				t.Fatal(got)
			}
			ready, e := applicationProgress(p, index, s.Applications, &ss[index-1], "")
			if tc.want != (ready && e == nil) {
				t.Fatal("readiness disagrees with full gate", ready, e)
			}
		})
	}
}

func TestInitialBaselineRejectsEarlierRevision(t *testing.T) {
	p, d, ss := revisionFixture(t)
	s := ss[0]
	observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "sync"))["revision"] = p.Phases[1].Revision
	s = proofSnapshot(s)
	gate := writeGateFixture(t, privateTemp(t), "baseline", p, p.Phases[0], s)
	if Assess(p, 0, s, nil, nil, d, gate).Passed() {
		t.Fatal("initial adoption accepted source equivalence")
	}
}

func TestDesiredIdentityStopsAtContentBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan, observation.Object, observation.Object)
		want   bool
	}{
		{"multiple unchanged publications", func(*Plan, observation.Object, observation.Object) {}, true},
		{"changed then reverted", func(p *Plan, _, _ observation.Object) {
			r := p.Revisions["detached-mixed"]
			r.FilesSHA256 = map[string]string{"gitops/test/leaf/kustomization.yaml": strings.Repeat("1", 64), "gitops/test/leaf/rendered.yaml": strings.Repeat("3", 64)}
			p.Revisions["detached-mixed"] = r
		}, false},
		{"unknown digest", func(p *Plan, _, _ observation.Object) {
			r := p.Revisions["detached-mixed"]
			r.FilesSHA256 = nil
			p.Revisions["detached-mixed"] = r
		}, false},
		{"future planned revision", func(p *Plan, a, _ observation.Object) {
			p.Revisions["future"] = Revision{Commit: strings.Repeat("a", 40), FilesSHA256: p.Revisions["baseline"].FilesSHA256}
			observation.Map(observation.At(a, "status", "sync"))["revision"] = strings.Repeat("a", 40)
		}, false},
		{"unplanned revision", func(_ *Plan, a, _ observation.Object) {
			observation.Map(observation.At(a, "status", "sync"))["revision"] = strings.Repeat("b", 40)
		}, false},
		{"intermediate App spec changed", func(p *Plan, _, _ observation.Object) {
			for i := range p.Phases[1].Applications {
				if p.Phases[1].Applications[i].Name == "argocd-self" {
					p.Phases[1].Applications[i].Spec = observation.Clone(p.Phases[1].Applications[i].Spec)
					p.Phases[1].Applications[i].Spec["project"] = "foreign"
				}
			}
		}, false},
		{"UID changed", func(_ *Plan, a, _ observation.Object) { observation.Map(a["metadata"])["uid"] = "replacement" }, false},
		{"prior spec changed", func(_ *Plan, _, b observation.Object) { observation.Map(b["spec"])["project"] = "foreign" }, false},
		{"initial adoption", func(p *Plan, _, _ observation.Object) { p.Phases = p.Phases[2:] }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, ss := revisionFixture(t)
			p.Phases[2].Revision = strings.Repeat("f", 40)
			p.Revisions["mixed-restored"] = Revision{Commit: p.Phases[2].Revision, FilesSHA256: p.Revisions["baseline"].FilesSHA256}
			var want observation.ExpectedApplication
			for _, a := range p.Phases[2].Applications {
				if a.Name == "argocd-self" {
					want = a
				}
			}
			want.Revision = p.Phases[2].Revision
			a := observation.Clone(rawIndex(ss[2].Applications)[AppRef(want.Name).Key()])
			prior := observation.Clone(a)
			observation.Map(observation.At(a, "status", "sync"))["revision"] = p.BaselineRevision
			tc.mutate(&p, a, prior)
			index := 2
			if tc.name == "initial adoption" {
				index = 0
			}
			if got := desiredEquivalent(p, index, want, a, prior); got != tc.want {
				t.Fatalf("equivalent=%v want=%v", got, tc.want)
			}
		})
	}
}

// F14: publishing the next unchanged revision must not invalidate an accepted
// leaf solely because it has now crossed two repository commits.
func TestDesiredIdentitySurvivesNextPublication(t *testing.T) {
	p, d, _ := revisionFixture(t)
	p.Phases[12].Revision = strings.Repeat("f", 40)
	for i := range p.Phases[12].Applications {
		p.Phases[12].Applications[i].Revision = p.Phases[12].Revision
	}
	p.Revisions["mixed-restored"] = Revision{Commit: p.Phases[12].Revision, FilesSHA256: p.Revisions["baseline"].FilesSHA256}
	ss := syntheticSnapshots(t, p, d)
	for _, index := range []int{11, 12} {
		s := ss[index]
		observation.Map(observation.At(rawIndex(s.Applications)[AppRef("argocd-self").Key()], "status", "sync"))["revision"] = p.BaselineRevision
		s = proofSnapshot(s)
		ss[index] = s
		ready, e := applicationProgress(p, index, s.Applications, &ss[index-1], "")
		if !ready || e != nil {
			t.Fatalf("phase %d invalidated unchanged desired identity: %v", index, e)
		}
		var gate *GateProof
		if p.Phases[index].Stage.AtlasGate {
			gate = writeGateFixture(t, privateTemp(t), "publication", p, p.Phases[index], s)
		}
		if got := Assess(p, index, s, &ss[0], &ss[index-1], d, gate); !got.Passed() {
			t.Fatal(got)
		}
	}
}
func TestStoredSemanticProofRejectsTamperedClosingRead(t *testing.T) {
	p, d, ss := revisionFixture(t)
	s := ss[1]
	observation.Map(s.ClosingApplications[0]["metadata"])["uid"] = "other"
	if Assess(p, 1, s, &ss[0], &ss[0], d, nil).Ownership == "VERIFIED" {
		t.Fatal("tampered inventory admitted")
	}
}
func TestLocalSourceClosureRejectsRemoteOrParentInput(t *testing.T) {
	for _, resource := range []string{"rendered.yaml", "../outside.yaml", "https://invalid/remote", "subdir"} {
		t.Run(resource, func(t *testing.T) {
			p, _, _ := revisionFixture(t)
			repo := privateTemp(t)
			dir := filepath.Join(repo, "gitops/test/leaf")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			k := observation.Object{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": []any{resource}}
			b := observation.Bytes(k)
			render := []byte("{}\n")
			for name, data := range map[string][]byte{"kustomization.yaml": b, "rendered.yaml": render} {
				if e := os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := git(context.Background(), repo, "init"); e != nil {
				t.Fatal(e)
			}
			sha, e := commitLocal(context.Background(), repo, "test local source closure")
			if e != nil {
				t.Fatal(e)
			}
			phase := p.Phases[0]
			phase.Revision = sha
			// Keep one unrelated app; other fixture sources are intentionally synthetic.
			phase.Applications = phase.Applications[:1]
			phase.Applications[0].Revision = sha
			p.Phases = []Phase{phase}
			p.Revisions = map[string]Revision{"baseline": {Commit: sha, FilesSHA256: map[string]string{"gitops/test/leaf/kustomization.yaml": observation.SHA(b), "gitops/test/leaf/rendered.yaml": observation.SHA(render)}}}
			e = validateSourceClosures(context.Background(), repo, p)
			if (e == nil) != (resource == "rendered.yaml") {
				t.Fatal("source closure validation", e)
			}
		})
	}
}
