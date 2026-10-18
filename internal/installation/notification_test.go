package installation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func notificationFixture(t *testing.T) (*Workflow, map[string]any, notificationIO, *int) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &Workflow{Config: Config{StateDirectory: dir}, Record: Record{FullCommit: strings.Repeat("a", 40), ClusterUID: "cluster"}}
	live := map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "test", "namespace": "platform-argocd", "uid": "app", "resourceVersion": "1", "annotations": map[string]any{"argocd.argoproj.io/tracking-id": "root:app"}}, "spec": map[string]any{"source": map[string]any{"targetRevision": "deployment"}}, "status": map[string]any{"sync": map[string]any{"revision": strings.Repeat("b", 40)}}}
	calls := new(int)
	io := notificationIO{
		fence: func(context.Context) error { return nil },
		get:   func(context.Context, string) (map[string]any, error) { return notificationClone(live), nil },
		patch: func(_ context.Context, _ string, b []byte) (map[string]any, error) {
			*calls++
			var ops []map[string]any
			if err := Decode(b, &ops); err != nil {
				t.Fatal(err)
			}
			if len(ops) != 4 || ops[0]["path"] != "/metadata/uid" || ops[0]["value"] != nested(live, "metadata", "uid") || ops[1]["path"] != "/metadata/resourceVersion" || ops[1]["value"] != nested(live, "metadata", "resourceVersion") || ops[2]["path"] != "/spec" || !reflect.DeepEqual(ops[2]["value"], live["spec"]) || ops[3]["path"] != "/metadata/annotations/argocd.argoproj.io~1refresh" || ops[3]["value"] != "hard" {
				t.Fatalf("unguarded request: %s", b)
			}
			mapping(nested(live, "metadata", "annotations"))[refreshKey] = "hard"
			mapping(live["metadata"])["resourceVersion"] = "done"
			return notificationClone(live), nil
		}, wait: func(context.Context, time.Duration) error { return nil },
	}
	return w, live, io, calls
}
func notificationClone(o map[string]any) map[string]any {
	var r map[string]any
	_ = Decode(JSON(o), &r)
	return r
}
func notificationDir(w *Workflow) string {
	return filepath.Join(w.Config.StateDirectory, "notifications", w.Record.FullCommit, "test")
}

func TestNotificationStatusRaceReobservesGuardedRequest(t *testing.T) {
	for _, code := range []int{409, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			w, live, io, calls := notificationFixture(t)
			want := notificationClone(live)
			patch := io.patch
			requests, fences := 0, 0
			io.fence = func(context.Context) error { fences++; return nil }
			io.patch = func(ctx context.Context, n string, b []byte) (map[string]any, error) {
				requests++
				if requests == 1 {
					mapping(live["metadata"])["resourceVersion"] = "2"
					mapping(live["metadata"])["managedFields"] = []any{map[string]any{"manager": "platform-argocd"}}
					live["status"] = map[string]any{"health": map[string]any{"status": "Progressing"}}
					return nil, notificationRejection{code, map[int]string{409: "Conflict", 422: "Invalid"}[code]}
				}
				return patch(ctx, n, b)
			}
			if err := w.notifyApplication(context.Background(), "test", want, io); err != nil {
				t.Fatal(err)
			}
			if requests != 2 || *calls != 1 || fences != 2 {
				t.Fatalf("requests=%d effects=%d fences=%d", requests, *calls, fences)
			}
			var result notificationResult
			b, err := privateRead(filepath.Join(notificationDir(w), "completed.json"))
			if err != nil || Decode(b, &result) != nil || result.Outcome != "acknowledged" {
				t.Fatal("missing completion")
			}
			// A new Workflow represents a separate invocation; completed requests never replay.
			again := *w
			if err := again.notifyApplication(context.Background(), "test", want, io); err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatal("replayed notification")
			}
		})
	}
}
func TestNotificationRejectsAuthorityChangesAndUnknownOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		change     func(map[string]any)
		err        error
		fenceFails bool
	}{
		{"same RV invalid", func(map[string]any) {}, notificationRejection{422, "Invalid"}, false},
		{"uid", func(o map[string]any) { mapping(o["metadata"])["uid"] = "replacement" }, notificationRejection{422, "Invalid"}, false},
		{"spec", func(o map[string]any) { mapping(o["spec"])["project"] = "other" }, notificationRejection{422, "Invalid"}, false},
		{"tracking", func(o map[string]any) {
			mapping(nested(o, "metadata", "annotations"))["argocd.argoproj.io/tracking-id"] = "different"
		}, notificationRejection{422, "Invalid"}, false},
		{"labels", func(o map[string]any) { mapping(o["metadata"])["labels"] = map[string]any{"authority": "other"} }, notificationRejection{422, "Invalid"}, false},
		{"deletion", func(o map[string]any) { mapping(o["metadata"])["deletionTimestamp"] = "now" }, notificationRejection{422, "Invalid"}, false},
		{"unknown refresh", func(o map[string]any) { mapping(nested(o, "metadata", "annotations"))[refreshKey] = "unknown" }, notificationRejection{422, "Invalid"}, false},
		{"unknown write", func(o map[string]any) { mapping(nested(o, "metadata", "annotations"))[refreshKey] = "hard" }, errors.New("response lost"), false},
		{"Git or cluster fence", func(map[string]any) {}, notificationRejection{422, "Invalid"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, live, io, _ := notificationFixture(t)
			want := notificationClone(live)
			calls, fences := 0, 0
			io.fence = func(context.Context) error {
				fences++
				if tc.fenceFails && fences > 1 {
					return errors.New("fence drift")
				}
				return nil
			}
			io.patch = func(context.Context, string, []byte) (map[string]any, error) {
				calls++
				if tc.name != "same RV invalid" {
					mapping(live["metadata"])["resourceVersion"] = "2"
				}
				tc.change(live)
				return nil, tc.err
			}
			if err := w.notifyApplication(context.Background(), "test", want, io); err == nil {
				t.Fatal("accepted unsafe retry")
			}
			if calls != 1 {
				t.Fatalf("requests=%d", calls)
			}
			// A later healthy read cannot turn an unconfirmed request into permission.
			live["status"] = map[string]any{"sync": map[string]any{"revision": w.Record.FullCommit}}
			if err := w.notifyApplication(context.Background(), "test", want, io); err == nil || calls != 1 {
				t.Fatal("unknown attempt replayed")
			}
		})
	}
}
func TestNotificationBudgetAndCoalescing(t *testing.T) {
	for _, mode := range []string{"budget", "existing refresh", "converged", "cancel", "disappeared"} {
		t.Run(mode, func(t *testing.T) {
			w, live, io, _ := notificationFixture(t)
			want := notificationClone(live)
			calls := 0
			io.patch = func(context.Context, string, []byte) (map[string]any, error) {
				calls++
				mapping(live["metadata"])["resourceVersion"] = fmt.Sprint(calls + 1)
				if mode == "existing refresh" {
					mapping(nested(live, "metadata", "annotations"))[refreshKey] = "normal"
				}
				if mode == "converged" {
					live["status"] = map[string]any{"sync": map[string]any{"revision": w.Record.FullCommit}}
				}
				return nil, notificationRejection{422, "Invalid"}
			}
			if mode == "cancel" {
				io.wait = func(context.Context, time.Duration) error { return context.Canceled }
			}
			if mode == "disappeared" {
				io.get = func(context.Context, string) (map[string]any, error) {
					if calls > 0 {
						return nil, nil
					}
					return notificationClone(live), nil
				}
			}
			err := w.notifyApplication(context.Background(), "test", want, io)
			success := mode == "existing refresh" || mode == "converged"
			if (err == nil) != success {
				t.Fatalf("success=%v err=%v", success, err)
			}
			wantCalls := 1
			if mode == "budget" {
				wantCalls = notificationAttempts
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d", calls)
			}
			if success {
				var r notificationResult
				b, e := privateRead(filepath.Join(notificationDir(w), "completed.json"))
				if e != nil || Decode(b, &r) != nil || r.Outcome != "coalesced-after-rejection" {
					t.Fatal("coalescing mislabeled")
				}
			}
		})
	}
}
func TestNotificationIntentBeforeEffectsAndAcknowledgement(t *testing.T) {
	for _, mode := range []string{"intent exists", "intent path blocked", "completion without intent", "bad ack", "receipt failure"} {
		t.Run(mode, func(t *testing.T) {
			w, live, io, calls := notificationFixture(t)
			want := notificationClone(live)
			dir := notificationDir(w)
			switch mode {
			case "intent exists":
				if err := save(filepath.Join(dir, "intent.json"), JSON(notificationIntent{Commit: w.Record.FullCommit}), true); err != nil {
					t.Fatal(err)
				}
			case "intent path blocked":
				if err := os.WriteFile(filepath.Join(w.Config.StateDirectory, "notifications"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "completion without intent":
				if err := save(filepath.Join(dir, "completed.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			default:
				patch := io.patch
				io.patch = func(ctx context.Context, n string, b []byte) (map[string]any, error) {
					if _, err := privateRead(filepath.Join(dir, "intent.json")); err != nil {
						t.Fatal("effect before durable intent")
					}
					if _, err := privateRead(filepath.Join(dir, "01-request.json")); err != nil {
						t.Fatal("effect before durable request")
					}
					result, err := patch(ctx, n, b)
					if mode == "bad ack" {
						mapping(result["metadata"])["uid"] = "other"
					} else if e := os.Mkdir(filepath.Join(dir, "completed.json"), 0700); e != nil {
						t.Fatal(e)
					}
					return result, err
				}
			}
			if err := w.notifyApplication(context.Background(), "test", want, io); err == nil {
				t.Fatal("expected STOP")
			}
			n := 0
			if mode == "bad ack" || mode == "receipt failure" {
				n = 1
			}
			if *calls != n {
				t.Fatalf("effects=%d", *calls)
			}
		})
	}
}
func TestNotificationNoWriteWhenAlreadyScheduledOrAbsent(t *testing.T) {
	for _, mode := range []string{"normal", "hard", "current", "absent"} {
		t.Run(mode, func(t *testing.T) {
			w, live, io, calls := notificationFixture(t)
			want := notificationClone(live)
			if mode == "absent" {
				io.get = func(context.Context, string) (map[string]any, error) { return nil, nil }
			} else if mode == "current" {
				mapping(nested(live, "status", "sync"))["revision"] = w.Record.FullCommit
			} else {
				mapping(nested(live, "metadata", "annotations"))[refreshKey] = mode
			}
			if err := w.notifyApplication(context.Background(), "test", want, io); err != nil {
				t.Fatal(err)
			}
			if *calls != 0 {
				t.Fatal("unnecessary write")
			}
		})
	}
}
