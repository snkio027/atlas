package installation

import (
	"atlas-refactor/internal/observation"
	"atlas-refactor/internal/platformnames"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"time"
)

const notificationAttempts = 5
const refreshKey = "argocd.argoproj.io/refresh"

// A private seam for deterministic interleavings, not an exported execution API.
type notificationIO struct {
	fence func(context.Context) error
	get   func(context.Context, string) (map[string]any, error)
	patch func(context.Context, string, []byte) (map[string]any, error)
	wait  func(context.Context, time.Duration) error
}

func (w *Workflow) notify(ctx context.Context, f Files) error {
	apps, err := applications(f)
	if err != nil {
		return err
	}
	c, err := w.notificationClient(ctx)
	if err != nil {
		return err
	}
	defer c.client.CloseIdleConnections()
	io := notificationIO{
		fence: func(ctx context.Context) error {
			remote, err := w.remote(ctx)
			if err != nil {
				return err
			}
			if remote != w.Record.FullCommit {
				return errors.New("Git changed before notification")
			}
			return c.fence(ctx)
		},
		get: func(ctx context.Context, name string) (map[string]any, error) {
			return c.request(ctx, http.MethodGet, notificationPath(name), nil)
		},
		patch: func(ctx context.Context, name string, body []byte) (map[string]any, error) {
			return c.request(ctx, http.MethodPatch, notificationPath(name)+"?fieldManager=atlas-install-notify", body)
		},
		wait: func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		},
	}
	names := make([]string, 0, len(apps))
	for name := range apps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = w.notifyApplication(ctx, name, apps[name], io); err != nil {
			return fmt.Errorf("notify %s: %w", name, err)
		}
	}
	return nil
}
func notificationPath(name string) string {
	return "/apis/argoproj.io/v1alpha1/namespaces/" + platformnames.Argo + "/applications/" + name
}

func notificationObject(o, want map[string]any, name, uid string) error {
	gotUID, _ := nested(o, "metadata", "uid").(string)
	rv, _ := nested(o, "metadata", "resourceVersion").(string)
	if o["apiVersion"] != "argoproj.io/v1alpha1" || o["kind"] != "Application" || nested(o, "metadata", "name") != name || nested(o, "metadata", "namespace") != platformnames.Argo || gotUID == "" || rv == "" || (uid != "" && gotUID != uid) || nested(o, "metadata", "deletionTimestamp") != nil {
		return errors.New("notification Application identity/deletion fence failed")
	}
	if !reflect.DeepEqual(o["spec"], want["spec"]) {
		return errors.New("Application spec drift before refresh")
	}
	return nil
}

// Only status and API bookkeeping may change between a rejected request and
// its re-observation. Tracking, labels, spec, owner references and deletion
// state remain exact. An existing recognized refresh is coalesced, never replaced.
func notificationAuthority(o map[string]any) map[string]any {
	var copy map[string]any
	_ = Decode(JSON(o), &copy)
	delete(copy, "status")
	m := mapping(copy["metadata"])
	delete(m, "resourceVersion")
	delete(m, "managedFields")
	if a, ok := m["annotations"].(map[string]any); ok {
		delete(a, refreshKey)
		if len(a) == 0 {
			delete(m, "annotations")
		}
	}
	return copy
}

type notificationResult struct {
	Intent  notificationIntent `json:"intent"`
	Outcome string             `json:"outcome"`
}

type notificationIntent struct {
	Commit     string `json:"commit"`
	ClusterUID string `json:"clusterUID"`
	UID        string `json:"uid"`
	SpecSHA256 string `json:"specSHA256"`
}

func (w *Workflow) notifyApplication(ctx context.Context, name string, want map[string]any, io notificationIO) error {
	if !regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$`).MatchString(name) || !commit.MatchString(w.Record.FullCommit) || w.Record.ClusterUID == "" {
		return errors.New("invalid notification identity")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	dir := filepath.Join(w.Config.StateDirectory, "notifications", w.Record.FullCommit, name)
	intentPath, resultPath := filepath.Join(dir, "intent.json"), filepath.Join(dir, "completed.json")
	var priorIntent *notificationIntent
	if b, err := privateRead(intentPath); err == nil {
		var v notificationIntent
		if err = Decode(b, &v); err != nil {
			return err
		}
		priorIntent = &v
		// Even a now-healthy Application cannot prove an unknown request did not
		// execute. A missing acknowledgement never reacquires write authority.
		result, err := privateRead(resultPath)
		var receipt notificationResult
		if err != nil || Decode(result, &receipt) != nil || receipt.Intent != v || (receipt.Outcome != "acknowledged" && receipt.Outcome != "coalesced-after-rejection") {
			return errors.New("prior notification outcome unconfirmed; no replay")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if _, err := os.Lstat(resultPath); !os.IsNotExist(err) {
		return errors.New("notification completion without intent")
	}
	var previous map[string]any
	uid := ""
	for attempt := 1; attempt <= notificationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Slow Git checks precede the fresh GET, minimizing the CAS window.
		if err := io.fence(ctx); err != nil {
			return err
		}
		live, err := io.get(ctx, name)
		if err != nil {
			return err
		}
		if live == nil {
			if previous != nil || priorIntent != nil {
				return errors.New("notification Application disappeared")
			}
			return nil // New child creation remains Argo's job and the final Gate's proof.
		}
		if err = notificationObject(live, want, name, uid); err != nil {
			return err
		}
		if uid == "" {
			uid = nested(live, "metadata", "uid").(string)
		}
		intent := notificationIntent{Commit: w.Record.FullCommit, ClusterUID: w.Record.ClusterUID, UID: uid, SpecSHA256: Digest(JSON(want["spec"]))}
		if priorIntent != nil {
			if *priorIntent != intent {
				return errors.New("saved notification binding differs")
			}
			return nil
		}
		if previous != nil {
			if nested(previous, "metadata", "resourceVersion") == nested(live, "metadata", "resourceVersion") || !reflect.DeepEqual(notificationAuthority(previous), notificationAuthority(live)) {
				return errors.New("rejected notification is not explained by benign RV churn")
			}
		}
		refresh := nested(live, "metadata", "annotations", refreshKey)
		if refresh != nil && refresh != "normal" && refresh != "hard" {
			return errors.New("unrecognized existing refresh request")
		}
		if nested(live, "status", "sync", "revision") == w.Record.FullCommit || refresh != nil {
			if previous != nil {
				return save(resultPath, JSON(notificationResult{intent, "coalesced-after-rejection"}), true)
			}
			return nil
		}
		if attempt == 1 {
			if err = observation.CreatePrivate(intentPath, JSON(intent)); err != nil {
				return err
			}
		}
		patch := []map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": uid},
			{"op": "test", "path": "/metadata/resourceVersion", "value": nested(live, "metadata", "resourceVersion")},
			{"op": "test", "path": "/spec", "value": live["spec"]},
		}
		if nested(live, "metadata", "annotations") == nil {
			patch = append(patch, map[string]any{"op": "add", "path": "/metadata/annotations", "value": map[string]any{}})
		}
		patch = append(patch, map[string]any{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1refresh", "value": "hard"})
		// Persist the exact guarded request before each possible side effect.
		if err = observation.CreatePrivate(filepath.Join(dir, fmt.Sprintf("%02d-request.json", attempt)), JSON(patch)); err != nil {
			return err
		}
		result, err := io.patch(ctx, name, JSON(patch))
		if err == nil {
			if err = notificationObject(result, want, name, uid); err != nil {
				return err
			}
			if nested(result, "metadata", "annotations", refreshKey) != "hard" {
				return errors.New("notification acknowledgement did not contain requested annotation")
			}
			return save(resultPath, JSON(notificationResult{intent, "acknowledged"}), true)
		}
		var rejected notificationRejection
		if !errors.As(err, &rejected) {
			return err
		}
		if e := observation.CreatePrivate(filepath.Join(dir, fmt.Sprintf("%02d-rejected.json", attempt)), JSON(rejected)); e != nil {
			return errors.Join(err, e)
		}
		if attempt == notificationAttempts {
			return fmt.Errorf("notification conflict budget exhausted: %w", err)
		}
		previous = live
		if err = io.wait(ctx, time.Duration(1<<(attempt-1))*100*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("notification attempts exhausted")
}
