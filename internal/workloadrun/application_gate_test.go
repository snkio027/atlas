package workloadrun

import (
	"atlas-refactor/internal/observation"
	"atlas-refactor/internal/workload"
	"reflect"
	"strings"
	"testing"
)

func gateApp(name string) (observation.ExpectedApplication, Object) {
	rev := strings.Repeat("a", 40)
	spec := Object{"project": "platform-project"}
	want := observation.ExpectedApplication{Namespace: "platform-argocd", Name: name, Spec: spec, Revision: rev}
	live := Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": Object{"name": name, "namespace": "platform-argocd", "uid": name + "-uid", "resourceVersion": "1", "generation": float64(1), "annotations": Object{observation.TrackingAnnotation: "platform-control:argoproj.io/Application:platform-argocd/" + name}, "managedFields": []any{Object{"manager": "argocd-controller", "operation": "Apply", "fieldsV1": Object{"f:spec": Object{}}}}}, "spec": spec, "status": Object{"sync": Object{"revision": rev, "status": "Synced"}, "health": Object{"status": "Healthy"}}}
	return want, live
}
func gateSession(wants []observation.ExpectedApplication, introduced bool) *rolloutSession {
	prior, owners, uids := map[string]Object{}, map[string]string{}, map[string]string{}
	accepted := map[string]bool{}
	for _, w := range wants {
		owners[w.Name] = "platform-control"
		accepted[w.Revision] = true
		if !introduced {
			prior[appIdentity(w.Name)] = Object{"spec": w.Spec}
			uids[appIdentity(w.Name)] = w.Name + "-uid"
		}
	}
	return newRolloutSession(true, prior, owners, uids, accepted)
}
func checkApp(t *testing.T, s *rolloutSession, want observation.ExpectedApplication, live Object, state GateState) []observation.ApplicationFact {
	t.Helper()
	snapshot := map[string]Object{}
	if live != nil {
		snapshot[want.Name] = live
	}
	facts, decision := s.applications([]observation.ExpectedApplication{want}, snapshot)
	if decision.State != state {
		t.Fatalf("want %s, got %+v", state, decision)
	}
	return facts
}
func TestRolloutAllowsSkippedRepeatedAndPartialInitialObservations(t *testing.T) {
	// Every path ends at the same proof. No empty status / generation=1 edge is
	// mandatory; API readers can miss arbitrarily many intermediate snapshots.
	for _, sequence := range [][]string{
		{"absent", "empty", "partial", "progress", "ready"},
		{"ready"}, {"partial", "partial", "ready"}, {"absent", "absent", "ready"},
		{"empty", "progress", "progress", "ready"},
	} {
		t.Run(strings.Join(sequence, "-"), func(t *testing.T) {
			want, _ := gateApp("new")
			session := gateSession([]observation.ExpectedApplication{want}, true)
			for _, step := range sequence {
				_, app := gateApp(want.Name)
				mapping(app["metadata"])["generation"] = float64(4)
				state := gateWaiting
				switch step {
				case "absent":
					app = nil
				case "empty":
					delete(app, "status")
				case "partial":
					app["status"] = Object{"conditions": []any{}, "summary": Object{}, "sync": Object{"status": "Unknown", "comparedTo": Object{"source": Object{"repoURL": ""}, "destination": Object{}}}, "health": Object{"status": "Missing", "lastTransitionTime": "2026-10-08T00:00:00Z"}}
				case "progress":
					mapping(at(app, "status", "sync"))["status"] = "OutOfSync"
				case "ready":
					state = gateReady
				}
				before := string(workload.JSON(app))
				facts := checkApp(t, session, want, app, state)
				if step == "empty" || step == "partial" {
					if facts[0].Classification != observation.Unknown {
						t.Fatal("waiting fabricated proof")
					}
				}
				if before != string(workload.JSON(app)) {
					t.Fatal("input was mutated")
				}
			}
		})
	}
}
func TestRolloutRejectsIdentityLossAndComparisonRegression(t *testing.T) {
	for _, change := range []string{"replace", "disappear", "lost-status", "unknown-status", "wrong-owner", "lost-ssa"} {
		t.Run(change, func(t *testing.T) {
			want, app := gateApp("new")
			session := gateSession([]observation.ExpectedApplication{want}, true)
			checkApp(t, session, want, app, gateReady)
			switch change {
			case "replace":
				mapping(app["metadata"])["uid"] = "replacement"
			case "disappear":
				app = nil
			case "lost-status":
				delete(app, "status")
			case "unknown-status":
				mapping(at(app, "status", "sync"))["status"] = "Unknown"
			case "wrong-owner":
				mapping(at(app, "metadata", "annotations"))[observation.TrackingAnnotation] = "other"
			case "lost-ssa":
				delete(mapping(app["metadata"]), "managedFields")
			}
			checkApp(t, session, want, app, gateRejected)
		})
	}
	// Replacement is also forbidden before the first comparison ever completes.
	want, app := gateApp("new")
	delete(app, "status")
	session := gateSession([]observation.ExpectedApplication{want}, true)
	checkApp(t, session, want, app, gateWaiting)
	mapping(app["metadata"])["uid"] = "replacement"
	checkApp(t, session, want, app, gateRejected)
}
func TestRolloutDoesNotInitializeExistingOrClosedPhase(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, absent := range []bool{false, true} {
			want, app := gateApp("a")
			session := gateSession([]observation.ExpectedApplication{want}, !existing)
			if !existing {
				session.active = false
			}
			delete(app, "status")
			if absent {
				app = nil
			}
			checkApp(t, session, want, app, gateRejected)
		}
	}
}
func TestRolloutRejectsMalformedOrFailedInitialization(t *testing.T) {
	changes := map[string]func(Object){
		"null":                 func(a Object) { a["status"] = nil },
		"string":               func(a Object) { a["status"] = "bad" },
		"null sync":            func(a Object) { a["status"] = Object{"sync": nil} },
		"numeric sync":         func(a Object) { a["status"] = Object{"sync": Object{"status": 3}} },
		"conditions shape":     func(a Object) { a["status"] = Object{"conditions": Object{}} },
		"conditions entry":     func(a Object) { a["status"] = Object{"conditions": []any{"bad"}} },
		"reconciled":           func(a Object) { a["status"] = Object{"reconciledAt": "2026-10-08T00:00:00Z"} },
		"history":              func(a Object) { a["status"] = Object{"history": []any{}} },
		"failed":               func(a Object) { a["status"] = Object{"operationState": Object{"phase": "Failed"}} },
		"terminated":           func(a Object) { a["status"] = Object{"operationState": Object{"phase": "Terminating"}} },
		"error":                func(a Object) { a["status"] = Object{"conditions": []any{Object{"type": "ComparisonError"}}} },
		"foreign spec":         func(a Object) { a["spec"] = Object{"project": "foreign"} },
		"missing uid":          func(a Object) { delete(mapping(a["metadata"]), "uid") },
		"wrong owner":          func(a Object) { mapping(a["metadata"])["annotations"] = Object{} },
		"finalizer":            func(a Object) { mapping(a["metadata"])["finalizers"] = []any{"cascade"} },
		"malformed finalizers": func(a Object) { mapping(a["metadata"])["finalizers"] = "bad" },
		"deleting":             func(a Object) { mapping(a["metadata"])["deletionTimestamp"] = "now" },
		"unknown sha": func(a Object) {
			a["status"] = Object{"sync": Object{"revision": strings.Repeat("f", 40), "status": "Synced"}, "health": Object{"status": "Healthy"}}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			want, app := gateApp("new")
			delete(app, "status")
			change(app)
			checkApp(t, gateSession([]observation.ExpectedApplication{want}, true), want, app, gateRejected)
		})
	}
}
func TestRolloutTargetSpecConvergenceIsMonotoneAndPlanBounded(t *testing.T) {
	want, app := gateApp("existing")
	old := Object{"project": "previous-project"}
	session := gateSession([]observation.ExpectedApplication{want}, false)
	session.prior[appIdentity(want.Name)] = Object{"spec": old}
	app["spec"] = old
	facts := checkApp(t, session, want, app, gateWaiting)
	if facts[0].Classification != observation.Drifted {
		t.Fatal("target fact was rewritten")
	}
	app["spec"] = want.Spec
	checkApp(t, session, want, app, gateReady)
	app["spec"] = old
	checkApp(t, session, want, app, gateRejected)
	session = gateSession([]observation.ExpectedApplication{want}, false)
	session.prior[appIdentity(want.Name)] = Object{"spec": old}
	app["spec"] = Object{"project": "third"}
	checkApp(t, session, want, app, gateRejected)
}
func TestApplicationGateFatalAlwaysWinsOverProgressAndMissing(t *testing.T) {
	a, la := gateApp("a-progress")
	mapping(at(la, "status", "sync"))["status"] = "OutOfSync"
	b, lb := gateApp("b-fatal")
	mapping(lb["status"])["conditions"] = []any{Object{"type": "InvalidSpecError", "message": "private-diagnostic-canary"}}
	c, _ := gateApp("c-missing")
	var previous []observation.ApplicationFact
	for i := 0; i < 100; i++ {
		wants := []observation.ExpectedApplication{a, b, c}
		if i%2 == 1 {
			wants = []observation.ExpectedApplication{c, b, a}
		}
		facts, d := gateSession(wants, true).applications(wants, map[string]Object{a.Name: la, b.Name: lb})
		if d.State != gateRejected || !strings.Contains(d.err().Error(), "APPLICATION_ERROR") || strings.Contains(d.err().Error(), "private-diagnostic-canary") {
			t.Fatal(d)
		}
		if len(facts) != 3 || facts[0].Ref.Name != a.Name || facts[1].Ref.Name != b.Name {
			t.Fatal("incomplete evidence")
		}
		if i > 0 && !reflect.DeepEqual(previous, facts) {
			t.Fatal("order dependent proof")
		}
		previous = facts
	}
	if applicationConditions(map[string]Object{b.Name: lb})[b.Name][0]["message"] != "private-diagnostic-canary" {
		t.Fatal("lost private diagnostic")
	}
}
func TestApplicationGateExactInventory(t *testing.T) {
	want, app := gateApp("expected")
	for _, snapshot := range []map[string]Object{{"foreign": app}, {"expected": app, "foreign": app}} {
		_, d := gateSession([]observation.ExpectedApplication{want}, true).applications([]observation.ExpectedApplication{want}, snapshot)
		if d.State != gateRejected {
			t.Fatal("unexpected Application passed", d)
		}
	}
}
func TestResourceConvergenceRejectsThirdContentAndBackwardTransition(t *testing.T) {
	id := "rbac.authorization.k8s.io/RoleBinding/demo/use"
	old := Object{"kind": "RoleBinding", "subjects": []any{Object{"kind": "ServiceAccount", "name": "old", "namespace": "project-demo"}}}
	target := Object{"kind": "RoleBinding", "subjects": []any{Object{"kind": "ServiceAccount", "name": "new", "namespace": "project-demo"}}}
	s := newRolloutSession(true, map[string]Object{id: old}, nil, nil, nil)
	if _, ok := s.content(id, target, old).(Pending); !ok {
		t.Fatal("old desired content did not wait")
	}
	third := Object{"kind": "RoleBinding", "subjects": []any{Object{"kind": "ServiceAccount", "name": "foreign", "namespace": "project-demo"}}}
	if err := s.content(id, target, third); err == nil {
		t.Fatal("third content accepted")
	} else if _, ok := err.(Pending); ok {
		t.Fatal("drift waits")
	}
	if err := s.content(id, target, target); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.content(id, target, old).(Pending); ok {
		t.Fatal("regression waits")
	}
}

// Missing proof must never yield Ready, including snapshots with independently
// omitted fields. This complements realistic sequences rather than restating the
// classifier's enum switch.
func FuzzRolloutCannotInventApplicationProof(f *testing.F) {
	for i := 0; i < 256; i++ {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, mask uint8) {
		want, app := gateApp("new")
		if mask&1 != 0 {
			delete(app, "status")
		}
		if mask&2 != 0 {
			delete(mapping(app["metadata"]), "uid")
		}
		if mask&4 != 0 {
			delete(mapping(app["metadata"]), "resourceVersion")
		}
		if mask&8 != 0 {
			delete(mapping(app["metadata"]), "managedFields")
		}
		if mask&16 != 0 {
			delete(mapping(app["metadata"]), "annotations")
		}
		if mask&32 != 0 {
			app["spec"] = Object{}
		}
		if mask&64 != 0 {
			mapping(app["metadata"])["finalizers"] = []any{"cascade"}
		}
		if mask&128 != 0 {
			if app["status"] == nil {
				app["status"] = Object{}
			}
			mapping(app["status"])["conditions"] = []any{Object{"type": "SyncError"}}
		}
		_, d := gateSession([]observation.ExpectedApplication{want}, true).applications([]observation.ExpectedApplication{want}, map[string]Object{want.Name: app})
		if (d.State == gateReady) != (mask == 0) {
			t.Fatalf("mask %d: %+v", mask, d)
		}
	})
}

func TestRolloutCannotUseStaleComparisonAfterSourceChange(t *testing.T) {
	want, app := gateApp("existing")
	oldSpec := Object{"project": "platform-project", "source": Object{"path": "gitops/old"}, "destination": Object{"namespace": "platform-argocd"}}
	want.Spec = Object{"project": "platform-project", "source": Object{"path": "gitops/new"}, "destination": Object{"namespace": "platform-argocd"}}
	app["spec"] = want.Spec
	compared := Object{"source": oldSpec["source"], "destination": oldSpec["destination"]}
	mapping(at(app, "status", "sync"))["comparedTo"] = compared
	session := gateSession([]observation.ExpectedApplication{want}, false)
	session.prior[appIdentity(want.Name)] = Object{"spec": oldSpec}
	checkApp(t, session, want, app, gateWaiting)
	compared["source"] = want.Spec["source"]
	checkApp(t, session, want, app, gateReady)
}

func TestRolloutMalformedResourceStatusCannotHideBehindHealthyApp(t *testing.T) {
	for _, row := range []Object{
		{"status": "Synced", "health": "Degraded"},
		{"status": "Synced", "health": Object{"status": false}},
		{"status": false, "hook": true},
		{"status": "Synced", "hook": "true"},
	} {
		want, app := gateApp("a")
		mapping(app["status"])["resources"] = []any{row}
		checkApp(t, gateSession([]observation.ExpectedApplication{want}, false), want, app, gateRejected)
	}
}
