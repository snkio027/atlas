package workloadrun

import (
	"atlas-refactor/internal/installation"
	"atlas-refactor/internal/workload"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderKeepsLegacyAndDoesNotGrantNativeWrite(t *testing.T) {
	b := workload.Binding{Schema: 1, Kind: "CapabilityBinding", Project: "demo", Name: "web-api-storage", Workload: "web-api", Capability: "object-storage", Bucket: "uploads", Access: "read-write"}
	legacy := Object{"identities": []any{Object{"name": "legacy", "credentials": []any{Object{"accessKey": "old", "secretKey": "old"}}, "actions": []string{"Write:uploads"}}}}
	before := workload.JSON(legacy)
	keys := map[string]Key{b.ID(): {strings.Repeat("a", 32), strings.Repeat("b", 32)}}
	p, e := ExtendProvider(legacy, []workload.Binding{b}, keys)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(workload.JSON(legacy), before) || !bytes.Equal(workload.JSON(array(p["identities"])[0]), workload.JSON(array(legacy["identities"])[0])) {
		t.Fatal("legacy changed")
	}
	added := mapping(array(p["identities"])[1])
	if added["actions"] != nil || len(array(p["policies"])) != 1 {
		t.Fatal("native broad permissions granted")
	}
	policy := ProviderPolicy("uploads")
	raw := string(workload.JSON(policy))
	for _, term := range []string{"CreateBucket", "DeleteBucket", "PutBucket", "s3:*"} {
		if strings.Contains(raw, term) {
			t.Fatal("overbroad policy", term)
		}
	}
	again, e := ExtendProvider(p, []workload.Binding{b}, keys)
	if e != nil || !bytes.Equal(workload.JSON(p), workload.JSON(again)) {
		t.Fatal("not idempotent", e)
	}
	keys[b.ID()] = Key{strings.Repeat("x", 32), strings.Repeat("y", 32)}
	if _, e = ExtendProvider(p, []workload.Binding{b}, keys); e == nil {
		t.Fatal("implicit rotation accepted")
	}
}
func TestGitPublicationPreservesCompleteParent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w := &Workflow{Config: Config{StateDirectory: dir}}
	os.MkdirAll(w.repo(), 0700)
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = w.repo()
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return strings.TrimSpace(string(b))
	}
	run("init", "--bare")
	// Initial tree with an unrelated file that the S2 output must retain.
	blob := exec.Command("git", "hash-object", "-w", "--stdin")
	blob.Dir = w.repo()
	blob.Stdin = strings.NewReader("keep\n")
	out, e := blob.Output()
	if e != nil {
		t.Fatal(e)
	}
	run("update-index", "--add", "--cacheinfo", "100755", strings.TrimSpace(string(out)), "notes/keep.txt")
	tree := run("write-tree")
	c := exec.Command("git", "-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit-tree", tree, "-m", "base")
	c.Dir = w.repo()
	base, e := c.Output()
	if e != nil {
		t.Fatal(e)
	}
	parent := strings.TrimSpace(string(base))
	files := Files{"notes/keep.txt": []byte("keep\n"), "platform/projects/demo.json": []byte("{}\n")}
	revision, e := w.commit(ctx, files, parent, "S2 fixture")
	if e != nil {
		t.Fatal(e)
	}
	got, e := w.ReadTree(ctx, revision)
	if e != nil || !sameFiles(files, got) {
		t.Fatal("tree data lost", e)
	}
	if !strings.HasPrefix(run("ls-tree", revision, "notes/keep.txt"), "100755 blob ") {
		t.Fatal("unrelated executable mode changed")
	}
	if run("rev-parse", revision+"^") != parent {
		t.Fatal("parent lost")
	}
}
func TestObservationRejectsAddedPermissionsAndPodMembers(t *testing.T) {
	want := Object{"kind": "NetworkPolicy", "spec": Object{"podSelector": Object{"matchLabels": Object{}}, "policyTypes": []any{"Ingress"}}}
	live := Object{"kind": "NetworkPolicy", "spec": Object{"podSelector": Object{}, "policyTypes": []any{"Ingress"}}}
	if !matches(want, live) {
		t.Fatal("equivalent empty selector rejected")
	}
	mapping(live["spec"])["ingress"] = []any{Object{}}
	if matches(want, live) {
		t.Fatal("additional ingress accepted")
	}
	want = Object{"kind": "Deployment", "spec": Object{"containers": []any{Object{"name": "web"}}}}
	live = Object{"kind": "Deployment", "spec": Object{"containers": []any{Object{"name": "web"}, Object{"name": "extra"}}}}
	if matches(want, live) {
		t.Fatal("additional container accepted")
	}
}
func TestPrivateSaveNeverOverwritesImmutableOrFollowsLinks(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e := os.Chmod(d, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "evidence.json")
	if e := save(p, []byte("a"), true); e != nil {
		t.Fatal(e)
	}
	if e := save(p, []byte("b"), true); e == nil {
		t.Fatal("immutable overwritten")
	}
	link := filepath.Join(d, "link")
	os.Symlink(p, link)
	if _, e := regular(link, true); e == nil {
		t.Fatal("symlink followed")
	}
}
func TestPlanApprovalBindsTargetBeforeAnyIO(t *testing.T) {
	w := &Workflow{BinarySHA256: strings.Repeat("a", 64), Install: installation.Workflow{Record: installation.Record{InstallID: "install", ClusterUID: "cluster"}}}
	p := Plan{Schema: 1, CompilerSHA256: w.BinarySHA256, InstallID: "other"}
	if e := w.approve(p, workload.Digest(workload.JSON(p))); e == nil {
		t.Fatal("foreign install approved")
	}
}

func TestAbsentAuthoredAnnotationsPermitArgoTracking(t *testing.T) {
	want := Object{"apiVersion": "v1", "kind": "Service", "metadata": Object{"name": "metrics", "namespace": "platform-monitoring", "annotations": nil}, "spec": Object{"ports": []any{Object{"port": 8080}}}}
	got := Object{"apiVersion": "v1", "kind": "Service", "metadata": Object{"name": "metrics", "namespace": "platform-monitoring", "annotations": Object{"argocd.argoproj.io/tracking-id": "monitoring:/Service:monitoring/metrics"}}, "spec": Object{"ports": []any{Object{"port": 8080}}}}
	if !matches(want, got) {
		t.Fatal("null authored annotations are not an instruction to remove Argo tracking")
	}
	mapping(want["metadata"])["annotations"] = Object{"reviewed": "value"}
	if matches(want, got) {
		t.Fatal("explicit authored annotation was ignored")
	}
}

func TestKnownAPIOmissionsPreserveAuthoredValues(t *testing.T) {
	cases := []struct {
		name, api, kind string
		object          Object
		path            []string
		zero, changed   any
	}{
		{"service false", "v1", "Service", Object{"spec": Object{}}, []string{"spec", "publishNotReadyAddresses"}, false, true},
		{"pod hostNetwork", "apps/v1", "Deployment", Object{"spec": Object{"template": Object{"spec": Object{}}}}, []string{"spec", "template", "spec", "hostNetwork"}, false, true},
		{"pod hostIPC", "apps/v1", "DaemonSet", Object{"spec": Object{"template": Object{"spec": Object{}}}}, []string{"spec", "template", "spec", "hostIPC"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.object
			want["apiVersion"], want["kind"] = tc.api, tc.kind
			got := comparisonObject(want)
			parent := mapping(at(want, tc.path[:len(tc.path)-1]...))
			key := tc.path[len(tc.path)-1]
			parent[key] = tc.zero
			delete(mapping(at(got, tc.path[:len(tc.path)-1]...)), key)
			if !matches(want, got) {
				t.Fatal("omitted default rejected")
			}
			mapping(at(got, tc.path[:len(tc.path)-1]...))[key] = tc.changed
			if matches(want, got) {
				t.Fatal("changed value accepted")
			}
			delete(mapping(at(got, tc.path[:len(tc.path)-1]...)), key)
			parent[key] = tc.changed
			if matches(want, got) {
				t.Fatal("omission hid authored non-default")
			}
		})
	}
	for _, kind := range []string{"Deployment", "DaemonSet", "StatefulSet"} {
		for _, probe := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
			want := Object{"apiVersion": "apps/v1", "kind": kind, "spec": Object{"template": Object{"spec": Object{"containers": []any{Object{"name": "web", probe: Object{"initialDelaySeconds": 0}}}}}}}
			got := comparisonObject(want)
			p := mapping(mapping(array(at(got, "spec", "template", "spec", "containers"))[0])[probe])
			delete(p, "initialDelaySeconds")
			if !matches(want, got) {
				t.Fatal(kind, probe, "omitted zero rejected")
			}
			p["initialDelaySeconds"] = 1
			if matches(want, got) {
				t.Fatal(kind, probe, "nonzero accepted")
			}
		}
	}
	// The same field name elsewhere is not a Kubernetes defaulting rule.
	want := Object{"apiVersion": "example.test/v1", "kind": "Thing", "spec": Object{"hostNetwork": false, "initialDelaySeconds": 0}}
	got := Object{"apiVersion": "example.test/v1", "kind": "Thing", "spec": Object{}}
	if matches(want, got) {
		t.Fatal("generic zero omission accepted")
	}
}

func TestBindingSubjectDefaultDoesNotBroadenPermissions(t *testing.T) {
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		want := Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": kind, "subjects": []any{Object{"kind": "ServiceAccount", "name": "controller", "namespace": "sealed-secrets", "apiGroup": ""}}, "roleRef": Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "controller"}}
		got := comparisonObject(want)
		subject := mapping(array(got["subjects"])[0])
		delete(subject, "apiGroup")
		if !matches(want, got) {
			t.Fatal("empty core API group omission rejected")
		}
		subject["apiGroup"] = "rbac.authorization.k8s.io"
		if matches(want, got) {
			t.Fatal("foreign API group accepted")
		}
		delete(subject, "apiGroup")
		subject["namespace"] = "foreign"
		if matches(want, got) {
			t.Fatal("foreign subject accepted")
		}
		subject["namespace"] = "sealed-secrets"
		got["subjects"] = append(array(got["subjects"]), Object{"kind": "User", "name": "extra"})
		if matches(want, got) {
			t.Fatal("extra subject accepted")
		}
	}
}
