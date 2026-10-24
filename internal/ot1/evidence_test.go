package ot1

import (
	"context"
	"errors"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func syntheticPlan(t *testing.T) (Plan, map[string]observation.Object) {
	t.Helper()
	root, _ := filepath.Abs("../..")
	scope, stages, e := LoadContracts(root)
	if e != nil {
		t.Fatal(e)
	}
	p := Plan{Schema: 1, Ceremony: "ot1-foundation-split/v1", Target: observation.Target{Cluster: developmentprofile.OT1Cluster, Context: "kind-" + developmentprofile.OT1Cluster, ClusterUID: "synthetic-cluster", KubeconfigSHA256: strings.Repeat("a", 64)}, Implementation: observation.Implementation{Revision: strings.Repeat("b", 40), BinarySHA256: strings.Repeat("c", 64)}, Repository: developmentprofile.Repository, Branch: developmentprofile.OT1Revision, ScopeSHA256: ScopeSHA256, StagesSHA256: StagesSHA256, MaxStageSeconds: 300, Scope: scope, Revisions: map[string]Revision{}}
	desired := map[string]observation.Object{}
	b, e := os.ReadFile(filepath.Join(root, "experiments/foundation-ownership/fixtures/source/resources.json"))
	if e != nil {
		t.Fatal(e)
	}
	objects, e := platform.DecodeJSONManifests(b)
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range objects {
		desired[observation.Reference(o).Key()] = o
	}
	for _, name := range []string{"atlas-bootstrap", "platform-project", "workload-project"} {
		o := observation.Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject", "metadata": observation.Object{"namespace": "argocd", "name": name}, "spec": observation.Object{"description": "synthetic permission fixture"}}
		desired[observation.Reference(o).Key()] = o
	}
	d := DefaultProject()
	desired[observation.Reference(d).Key()] = d
	for _, s := range stages {
		sha := strings.Repeat("d", 40)
		phase := Phase{Stage: s, Revision: sha}
		for _, name := range []string{"atlas-refactor-root", "argocd-self", "platform-control", "project-bootstrap"} {
			phase.Applications = append(phase.Applications, observation.ExpectedApplication{Name: name, Spec: observation.Object{"fixture": "only local unit test"}, Revision: sha})
		}
		for _, name := range append([]string{Source}, Targets...) {
			if mode := s.Applications[name]; mode != "" {
				a, _ := Application(name, mode == "strict")
				spec := observation.Map(a["spec"])
				if s.AtlasGate {
					observation.Map(spec["syncPolicy"])["automated"] = observation.Object{"selfHeal": true, "prune": true}
				}
				phase.Applications = append(phase.Applications, observation.ExpectedApplication{Name: name, Spec: spec, Revision: sha})
			}
		}
		p.Phases = append(p.Phases, phase)
	}
	return p, desired
}
func live(o observation.Object, uid string) observation.Object {
	c := observation.Clone(o)
	m := observation.Map(c["metadata"])
	m["uid"] = uid
	m["resourceVersion"] = "1"
	m["managedFields"] = []any{observation.Object{"manager": "argocd-controller", "operation": "Apply", "fieldsV1": observation.Object{"f:spec": observation.Object{}}}}
	return c
}
func syntheticSnapshots(t *testing.T, plan Plan, desired map[string]observation.Object) []Snapshot {
	t.Helper()
	out := []Snapshot{}
	uids := map[string]string{}
	for i, phase := range plan.Phases {
		env := observation.Envelope{Schema: "atlas.observation/v1", Subject: "ot1/" + phase.Stage.Name, Target: plan.Target, ImplementationSHA: plan.Implementation.Revision, BinarySHA256: plan.Implementation.BinarySHA256, ExpectedRevision: phase.Revision, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), Classification: observation.Verified}
		s := Snapshot{Schema: 1, PlanSHA256: observation.Digest(plan), Stage: phase.Stage.Name, Revision: phase.Revision, Envelope: env}
		nextUIDs := map[string]string{}
		for _, expect := range phase.Applications {
			uid := uids[expect.Name]
			if uid == "" {
				uid = phase.Stage.Name + "-" + expect.Name
			}
			nextUIDs[expect.Name] = uid
			app := live(observation.Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": observation.Object{"name": expect.Name, "namespace": "argocd"}, "spec": expect.Spec}, uid)
			status := observation.Object{"sync": observation.Object{"status": "Synced", "revision": phase.Revision}, "health": observation.Object{"status": "Healthy"}}
			app["status"] = status
			if destination(expect.Name) != "" {
				resources := []any{}
				for _, item := range plan.Scope.Objects {
					if expect.Name == Source || expect.Name == item.NewOwner {
						ref, _ := ParseIdentity(item.Identity)
						group, version, ok := strings.Cut(ref.APIVersion, "/")
						if !ok {
							version = group
							group = ""
						}
						resources = append(resources, observation.Object{"group": group, "version": version, "kind": ref.Kind, "namespace": ref.Namespace, "name": ref.Name, "status": "Synced"})
					}
				}
				status["resources"] = resources
				if phase.Stage.AtlasGate {
					observation.Map(app["metadata"])["annotations"] = observation.Object{observation.TrackingAnnotation: observation.Tracking("platform-control", "argocd", AppRef(expect.Name))}
				}
			}
			if step := Steps(plan)[i]; step.Sync && step.Owner == expect.Name {
				op := observation.Object{"phase": "Succeeded", "finishedAt": "2026-09-27T00:00:00Z", "syncResult": observation.Object{"revision": phase.Revision}, "operation": observation.Object{"info": []any{observation.Object{"name": "ot1-stage", "value": phase.Stage.Name}}, "sync": observation.Object{"revision": phase.Revision, "prune": false, "syncOptions": observation.At(app, "spec", "syncPolicy", "syncOptions")}}}
				if phase.Stage.Outcome == "blocked" {
					op["phase"] = "Failed"
					op["message"] = "Shared resource found: synthetic probe"
					status["conditions"] = []any{observation.Object{"type": "SharedResourceWarning"}}
					observation.Map(status["sync"])["status"] = "OutOfSync"
				}
				status["operationState"] = op
				if phase.Stage.Outcome != "blocked" {
					status["reconciledAt"] = op["finishedAt"]
				}
			}
			s.Applications = append(s.Applications, app)
			s.Envelope.Raw = append(s.Envelope.Raw, app)
		}
		uids = nextUIDs
		for _, item := range plan.Scope.Objects {
			ref, _ := ParseIdentity(item.Identity)
			o := live(LiveDesired(desired[ref.Key()]), "stable-"+ref.Name+"-"+ref.Namespace)
			m := observation.Map(o["metadata"])
			annotations := observation.Map(m["annotations"])
			if annotations == nil {
				annotations = observation.Object{}
				m["annotations"] = annotations
			}
			annotations[observation.TrackingAnnotation] = observation.Tracking(ownerFor(phase.Stage, item), destination(ownerFor(phase.Stage, item)), ref)
			if ref.Kind == "Namespace" {
				o["status"] = observation.Object{"phase": "Active"}
			}
			s.Envelope.Raw = append(s.Envelope.Raw, o)
		}
		for _, ref := range identityRefs() {
			o := live(observation.Object{"apiVersion": ref.APIVersion, "kind": ref.Kind, "metadata": observation.Object{"name": ref.Name, "namespace": ref.Namespace}, "data": observation.Object{"binding": "synthetic"}}, "stable-"+ref.Name)
			s.Envelope.Raw = append(s.Envelope.Raw, o)
		}
		for _, name := range []string{"atlas-bootstrap", "platform-project", "workload-project", "default"} {
			ref := observation.Ref{APIVersion: "argoproj.io/v1alpha1", Kind: "AppProject", Namespace: "argocd", Name: name}
			s.Projects = append(s.Projects, live(desired[ref.Key()], "project-"+name))
		}
		for _, suffix := range []string{"control-plane", "worker", "worker2", "worker3"} {
			s.Nodes = append(s.Nodes, live(observation.Object{"apiVersion": "v1", "kind": "Node", "metadata": observation.Object{"name": plan.Target.Cluster + "-" + suffix}, "status": observation.Object{"conditions": []any{observation.Object{"type": "Ready", "status": "True"}}}}, "node-"+suffix))
		}
		if i > 0 {
			s.Envelope.Classification = observation.Progressing
		} // verifier must reclassify raw facts, not trust caller PASS.
		out = append(out, s)
	}
	return out
}
func fixtureGate(plan Plan, phase Phase, s Snapshot) *GateProof {
	hashes := map[string]string{}
	for _, n := range []string{"status.json", "repeat-apply.json", "audit-before.json", "audit-after.json", "runtime.json"} {
		hashes[n] = strings.Repeat("a", 64)
	}
	return &GateProof{validated: true, Stage: phase.Stage.Name, PlanSHA256: observation.Digest(plan), SnapshotSHA256: observation.Digest(s), EngineStatus: "ADOPTED", RuntimeVerified: true, EvidenceFiles: hashes}
}
func cloneSnapshot(t *testing.T, s Snapshot) Snapshot {
	t.Helper()
	var out Snapshot
	if e := observation.Decode(observation.Bytes(s), &out, true); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestAll29StatesAndNegativeOwnershipEvidence(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	for i, s := range snapshots {
		var base, previous *Snapshot
		if i > 0 {
			base = &snapshots[0]
			previous = &snapshots[i-1]
		}
		var gate *GateProof
		if plan.Phases[i].Stage.AtlasGate {
			gate = fixtureGate(plan, plan.Phases[i], s)
		}
		got := Assess(plan, i, s, base, previous, desired, gate)
		if !got.Passed() {
			t.Fatalf("%s: %+v", s.Stage, got)
		}
	}
	tests := []struct {
		name   string
		index  int
		mutate func(*Snapshot)
	}{
		{"wrong desired revision", 3, func(s *Snapshot) { s.Revision = strings.Repeat("a", 40) }},
		{"application list-get version fence changed", 7, func(s *Snapshot) { s.InventoryError = "APPLICATION_CHANGED_DURING_CAPTURE" }},
		{"read unavailable", 3, func(s *Snapshot) { s.InventoryError = "timeout" }},
		{"closing fence changed", 3, func(s *Snapshot) { s.Envelope.Reasons = []string{"SNAPSHOT_CHANGED"} }},
		{"object recreated", 3, func(s *Snapshot) { observation.Map(s.Envelope.Raw[len(s.Applications)]["metadata"])["uid"] = "new" }},
		{"namespace label drift", 3, func(s *Snapshot) {
			for _, o := range s.Envelope.Raw {
				if observation.Reference(o).Kind == "Namespace" {
					observation.Map(observation.At(o, "metadata", "labels"))["evil"] = "yes"
					break
				}
			}
		}},
		{"ownership wrong domain", 3, func(s *Snapshot) {
			o := s.Envelope.Raw[len(s.Applications)]
			observation.Map(observation.At(o, "metadata", "annotations"))[observation.TrackingAnnotation] = "other:wrong"
		}},
		{"extra application", 3, func(s *Snapshot) { s.Applications = append(s.Applications, observation.Clone(s.Applications[0])) }},
		{"project permission drift", 3, func(s *Snapshot) { observation.Map(s.Projects[1]["spec"])["extra"] = "permission" }},
		{"stale operation stage", 3, func(s *Snapshot) {
			for _, app := range s.Applications {
				if observation.Reference(app).Name == "secrets-foundation" {
					observation.Map(observation.At(app, "status", "operationState", "operation"))["info"] = []any{observation.Object{"name": "ot1-stage", "value": "old"}}
				}
			}
		}},
		{"unexpected error alongside expected refusal", 2, func(s *Snapshot) {
			for _, app := range s.Applications {
				if observation.Reference(app).Name == "secrets-foundation" {
					status := observation.Map(app["status"])
					status["conditions"] = append(observation.Slice(status["conditions"]), observation.Object{"type": "SyncError"})
				}
			}
		}},
		{"unexpected strict success", 2, func(s *Snapshot) {
			for _, app := range s.Applications {
				if observation.Reference(app).Name == "secrets-foundation" {
					observation.Map(observation.At(app, "status", "operationState"))["phase"] = "Succeeded"
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bad := cloneSnapshot(t, snapshots[test.index])
			test.mutate(&bad)
			got := Assess(plan, test.index, bad, &snapshots[0], &snapshots[test.index-1], desired, nil)
			if got.Ownership != "STOP" {
				t.Fatalf("bad evidence passed: %+v", got)
			}
		})
	}
	gate := fixtureGate(plan, plan.Phases[0], snapshots[0])
	gate.validated = false
	if got := Assess(plan, 0, snapshots[0], nil, nil, desired, gate); got.Atlas == "VERIFIED" {
		t.Fatal("unchecked caller gate assertion accepted")
	}
}
func TestGuardedApplicationRequests(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	find := func(i int) observation.Object {
		owner := *plan.Phases[i].Stage.ActiveOwner
		return rawIndex(snapshots[i].Applications)[AppRef(owner).Key()]
	}
	current := find(2)
	uid := observation.String(observation.At(current, "metadata", "uid"))
	patch, e := ModePatch(current, uid, plan.Phases[2], plan.Phases[3])
	if e != nil {
		t.Fatal(e)
	}
	if len(patch) != 4 || patch[3]["path"] != "/spec/syncPolicy/syncOptions" {
		t.Fatal("mode patch expands mutation surface")
	}
	for _, mutate := range []func(observation.Object){
		func(o observation.Object) { observation.Map(o["metadata"])["uid"] = "recreated" },
		func(o observation.Object) { o["operation"] = observation.Object{"sync": observation.Object{}} },
		func(o observation.Object) {
			observation.Map(o["metadata"])["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
		},
		func(o observation.Object) { observation.Map(o["spec"])["project"] = "other-project" },
		func(o observation.Object) { observation.Map(observation.At(o, "status", "sync"))["revision"] = "stale" },
		func(o observation.Object) {
			observation.Map(observation.At(o, "status", "operationState", "operation", "sync"))["prune"] = true
		},
	} {
		bad := observation.Clone(current)
		mutate(bad)
		if _, e = ModePatch(bad, uid, plan.Phases[2], plan.Phases[3]); e == nil {
			t.Fatal("unsafe mode patch accepted")
		}
	}
	changed := observation.Clone(current)
	observation.Map(observation.At(changed, "spec", "syncPolicy"))["syncOptions"] = Options(false)
	sync, e := SyncPatch(changed, uid, plan.Phases[3])
	if e != nil {
		t.Fatal(e)
	}
	if sync[len(sync)-1]["path"] != "/operation" {
		t.Fatal("sync patch path")
	}
	if _, e = SyncPatch(find(3), uid, plan.Phases[3]); e == nil {
		t.Fatal("operation resubmission allowed")
	}
	expected, _ := Application("secrets-foundation", true)
	url, body, e := DeleteRequest(current, expected, uid, plan.Phases[2].Revision)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(url, "/secrets-foundation") || body["propagationPolicy"] != "Orphan" || observation.At(body, "preconditions", "uid") != uid || observation.At(body, "preconditions", "resourceVersion") != "1" {
		t.Fatal("delete is not atomically guarded")
	}
	if _, e = CreateApplication(plan.Phases[2], "storage-foundation", true); e == nil {
		t.Fatal("wrong create owner")
	}
	if _, e = CreateApplication(plan.Phases[2], "secrets-foundation", false); e == nil {
		t.Fatal("unknown treated as absence")
	}
}
func TestStoppedAttemptsCannotAdvanceOrOverwrite(t *testing.T) {
	plan, desired := syntheticPlan(t)
	s := syntheticSnapshots(t, plan, desired)[0]
	base, _ := filepath.EvalSymlinks(t.TempDir())
	if e := os.Chmod(base, 0700); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(base, "attempt")
	a, e := NewAttempt(plan, dir)
	if e != nil {
		t.Fatal(e)
	}
	// Missing independent Atlas proof is a terminal STOP, even with valid OT-1A.
	cp, e := a.Record(s, desired, nil)
	if e != nil {
		t.Fatal(e)
	}
	if cp.Assessment.Ownership != "VERIFIED" || cp.Assessment.Atlas != "NOT_PROVEN" {
		t.Fatalf("gates conflated: %+v", cp)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "terminal.json"))
	if _, e = a.Next(); e == nil {
		t.Fatal("STOP advanced")
	}
	if _, e = a.Record(s, desired, nil); e == nil {
		t.Fatal("STOP overwritten")
	}
	if _, e = NewAttempt(plan, dir); e == nil {
		t.Fatal("attempt reused")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "terminal.json"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("history changed")
	}
}

type neverRun struct{ calls int }

func (n *neverRun) Run(context.Context, atlas.Request) ([]byte, error) {
	n.calls++
	return nil, errors.New("test delegate")
}
func TestReadOnlyRepeatApplyGuard(t *testing.T) {
	d := &neverRun{}
	r := ReadOnlyRunner{Delegate: d}
	for _, q := range []atlas.Request{{Tool: "kubectl", Args: []string{"apply", "-f", "-"}}, {Tool: "kind", Args: []string{"create", "cluster"}}, {Tool: "docker", Args: []string{"exec", "node", "sh"}}, {Tool: "git", Args: []string{"push", "origin", "main"}}, {Tool: "kubectl", Args: []string{"--kubeconfig", "private", "--context", "bound", "--request-timeout=30s", "create", "--dry-run=server", "--validate=false", "-f", "-", "-o", "json"}, Input: []byte("{}")}} {
		if _, e := r.Run(context.Background(), q); e == nil {
			t.Fatal("write passed")
		}
	}
	if d.calls != 0 || r.Denied != 5 {
		t.Fatal("denied request reached executor")
	}
	q := atlas.Request{Tool: "kubectl", Args: []string{"--kubeconfig", "private", "--context", "bound", "--request-timeout=30s", "create", "--dry-run=client", "--validate=false", "-f", "-", "-o", "json"}, Input: []byte("{}")}
	if !ReadOnlyRequest(q) {
		t.Fatal("existing engine's purely client-side manifest decode denied")
	}
}

func TestArgoOperationRoundTripOmitsFalseButCannotOverrideGitSource(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	phase := plan.Phases[2]
	app := observation.Clone(rawIndex(snapshots[2].Applications)[AppRef(*phase.Stage.ActiveOwner).Key()])
	sync := observation.Map(observation.At(app, "status", "operationState", "operation", "sync"))
	delete(sync, "prune")
	if e := operationMatches(app, phase, phase.Stage.Name, phase.Stage.Outcome); e != nil {
		t.Fatal("Go omitempty round trip rejected", e)
	}
	for _, field := range []string{"source", "sources", "manifests", "resources", "dryRun", "syncStrategy", "unrecognized"} {
		bad := observation.Clone(app)
		observation.Map(observation.At(bad, "status", "operationState", "operation", "sync"))[field] = true
		if e := operationMatches(bad, phase, phase.Stage.Name, phase.Stage.Outcome); e == nil {
			t.Fatal("unreviewed operation override accepted", field)
		}
	}
	for _, value := range []any{true, "false", 0, nil} {
		bad := observation.Clone(app)
		observation.Map(observation.At(bad, "status", "operationState", "operation", "sync"))["prune"] = value
		if e := operationMatches(bad, phase, phase.Stage.Name, phase.Stage.Outcome); e == nil {
			t.Fatal("malformed/enabled prune accepted", value)
		}
	}
}

// Argo can compare unchanged manifests at B without performing another sync:
// status.sync.revision=B while the successful operation still records A.
func TestObservedStateDoesNotInventCeremonyOperation(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	for _, name := range []string{"BASELINE_ADOPTED", "FORWARD_SECRETS_STRICT_RESTORED"} {
		t.Run(name, func(t *testing.T) {
			index := -1
			for i, p := range plan.Phases {
				if p.Stage.Name == name {
					index = i
				}
			}
			if index < 0 {
				t.Fatal("missing fixture phase")
			}
			s := snapshots[index]
			owner := *plan.Phases[index].Stage.ActiveOwner
			app := rawIndex(s.Applications)[AppRef(owner).Key()]
			observation.Map(app["status"])["operationState"] = observation.Object{
				"phase": "Succeeded", "finishedAt": "2026-09-27T00:00:00Z",
				"syncResult": observation.Object{"revision": strings.Repeat("a", 40)},
				"operation":  observation.Object{"sync": observation.Object{"revision": strings.Repeat("a", 40)}},
			}
			var base, previous *Snapshot
			if index > 0 {
				base, previous = &snapshots[0], &snapshots[index-1]
			}
			ready, e := applicationProgress(plan, index, s.Applications, previous, "")
			got := Assess(plan, index, s, base, previous, desired, fixtureGate(plan, plan.Phases[index], s))
			if name == "BASELINE_ADOPTED" {
				if !ready || e != nil || !got.Passed() {
					t.Fatalf("valid observed baseline rejected: ready=%v err=%v assessment=%+v", ready, e, got)
				}
				// State-only evidence still requires exact observed revision and idle health.
				for _, change := range []string{"revision", "failed", "active", "unhealthy"} {
					bad := cloneSnapshot(t, s)
					for _, objects := range [][]observation.Object{bad.Applications, bad.Envelope.Raw} {
						o := rawIndex(objects)[AppRef(owner).Key()]
						switch change {
						case "revision":
							observation.Map(observation.At(o, "status", "sync"))["revision"] = strings.Repeat("a", 40)
						case "failed":
							observation.Map(observation.At(o, "status", "operationState"))["phase"] = "Failed"
						case "active":
							o["operation"] = observation.Object{"sync": observation.Object{"revision": plan.Phases[index].Revision}}
						case "unhealthy":
							observation.Map(observation.At(o, "status", "health"))["status"] = "Degraded"
						}
					}
					ready, _ := applicationProgress(plan, index, bad.Applications, previous, "")
					got := Assess(plan, index, bad, base, previous, desired, fixtureGate(plan, plan.Phases[index], bad))
					if ready || got.Ownership != "STOP" {
						t.Fatalf("%s baseline accepted: ready=%v assessment=%+v", change, ready, got)
					}
				}
			} else if ready || e == nil || got.Ownership != "STOP" || !strings.Contains(strings.Join(got.Reasons, ","), "OPERATION_FENCE:"+owner) {
				t.Fatalf("uncorrelated transition accepted: ready=%v err=%v assessment=%+v", ready, e, got)
			}
		})
	}
}

func TestBaselineSelectorWireOmissionIsNarrow(t *testing.T) {
	plan, desired := syntheticPlan(t)
	snapshots := syntheticSnapshots(t, plan, desired)
	ref := observation.Ref{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Namespace: "atlas-monitoring", Name: "monitoring-ingress"}
	peer := func(o observation.Object) observation.Object {
		rule := observation.Map(observation.Slice(observation.At(o, "spec", "ingress"))[0])
		return observation.Map(observation.Slice(rule["from"])[0])
	}
	wire := cloneSnapshot(t, snapshots[0])
	livePolicy := rawIndex(wire.Envelope.Raw)[ref.Key()]
	delete(observation.Map(peer(livePolicy)["podSelector"]), "matchLabels")
	before := observation.Digest(wire)
	got := Assess(plan, 0, wire, nil, nil, desired, fixtureGate(plan, plan.Phases[0], wire))
	if !got.Passed() || observation.Digest(wire) != before {
		t.Fatalf("valid empty selector wire form rejected/evidence mutated: %+v", got)
	}
	for _, tc := range []struct {
		name   string
		mutate func(observation.Object)
	}{
		{"nonempty labels", func(o observation.Object) {
			peer(o)["podSelector"] = observation.Object{"matchLabels": observation.Object{"changed": "true"}}
		}},
		{"label expressions", func(o observation.Object) {
			peer(o)["podSelector"] = observation.Object{"matchExpressions": []any{observation.Object{"key": "tenant", "operator": "Exists"}}}
		}},
		{"selector absent", func(o observation.Object) { delete(peer(o), "podSelector") }},
		{"selector null", func(o observation.Object) { peer(o)["podSelector"] = nil }},
		{"labels null", func(o observation.Object) { peer(o)["podSelector"] = observation.Object{"matchLabels": nil} }},
		{"labels wrong type", func(o observation.Object) { peer(o)["podSelector"] = observation.Object{"matchLabels": []any{}} }},
		{"policy type", func(o observation.Object) { observation.Map(o["spec"])["policyTypes"] = []any{"Ingress", "Egress"} }},
		{"ports", func(o observation.Object) {
			observation.Map(observation.Slice(observation.At(o, "spec", "ingress"))[0])["ports"] = []any{observation.Object{"protocol": "TCP", "port": 1}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cloneSnapshot(t, wire)
			tc.mutate(rawIndex(bad.Envelope.Raw)[ref.Key()])
			got := Assess(plan, 0, bad, nil, nil, desired, fixtureGate(plan, plan.Phases[0], bad))
			if got.Ownership != "STOP" || !strings.Contains(strings.Join(got.Reasons, ","), "BASELINE_CONTENT_NOT_GIT_DEFINED:"+ref.Key()) {
				t.Fatalf("semantic change hidden: %+v", got)
			}
		})
	}
	original := desired[ref.Key()]
	if observation.Digest(baselineContent(original)) != observation.Digest(baselineContent(livePolicy)) {
		t.Fatal("equivalent representations differ")
	}
	outside := observation.Clone(original)
	outside["apiVersion"] = "example.io/v1"
	if observation.Digest(baselineContent(outside)) != observation.Digest(observation.Semantic(outside)) {
		t.Fatal("normalization escaped known NetworkPolicy GVK")
	}
}

func TestNetworkPolicyEmptySelectorsKeepPeerAndPlacement(t *testing.T) {
	desired := observation.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": observation.Object{"name": "policy", "namespace": "test"}, "spec": observation.Object{"podSelector": observation.Object{"matchLabels": observation.Object{}}, "egress": []any{observation.Object{"to": []any{observation.Object{"namespaceSelector": observation.Object{"matchLabels": observation.Object{}}, "podSelector": observation.Object{"matchLabels": observation.Object{"app": "db"}}}}}}}}
	actual := observation.Clone(desired)
	observation.Map(actual["spec"])["podSelector"] = observation.Object{}
	peer := observation.Map(observation.Slice(observation.Map(observation.Slice(observation.At(actual, "spec", "egress"))[0])["to"])[0])
	peer["namespaceSelector"] = observation.Object{}
	if observation.Digest(baselineContent(desired)) != observation.Digest(baselineContent(actual)) {
		t.Fatal("empty top-level/egress namespace selectors differ")
	}
	delete(peer, "namespaceSelector")
	if observation.Digest(baselineContent(desired)) == observation.Digest(baselineContent(actual)) {
		t.Fatal("absence of namespace selector was hidden")
	}
}

func TestReadOnlySeedInventoryWithStdin(t *testing.T) {
	q := atlas.Request{Tool: "kubectl", Args: []string{"--kubeconfig", "private", "--context", "bound", "--request-timeout=30s", "get", "-f", "-", "--ignore-not-found=true", "--show-managed-fields", "-o", "json"}, Input: []byte("{\"kind\":\"List\",\"items\":[]}")}
	if !ReadOnlyRequest(q) {
		t.Fatal("normal engine Seed inventory GET was denied")
	}
	for _, change := range []func(*atlas.Request){
		func(r *atlas.Request) { r.Args[5] = "apply" },
		func(r *atlas.Request) { r.Args[5] = "create" },
		func(r *atlas.Request) { r.Args[5] = "delete" },
		func(r *atlas.Request) { r.Args[7] = "unreviewed-file" },
		func(r *atlas.Request) { r.Args[8] = "--raw=/api/v1/namespaces" },
		func(r *atlas.Request) { r.Args = append(r.Args, "--watch") },
		func(r *atlas.Request) { r.InputPath = "file" },
		func(r *atlas.Request) { r.Tool = "other" },
	} {
		bad := q
		bad.Args = append([]string(nil), q.Args...)
		change(&bad)
		if ReadOnlyRequest(bad) {
			t.Fatalf("stdin exception admitted another request: %v", bad.Args)
		}
	}
}
