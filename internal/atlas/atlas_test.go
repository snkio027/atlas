package atlas

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type simulator struct {
	app                  *App
	exists, converge     bool
	objects              map[string]Object
	effects              []string
	failRead, failCreate string
	cancelAfterRoot      context.CancelFunc
}

func key(kind, ns, name string) string { return strings.ToLower(kind) + "/" + ns + "/" + name }
func (s *simulator) put(o Object) {
	m := o["metadata"].(Object)
	if m["uid"] == nil {
		m["uid"] = "uid-" + m["name"].(string)
	}
	ns, _ := m["namespace"].(string)
	s.objects[key(o["kind"].(string), ns, m["name"].(string))] = o
}
func argValue(args []string, k string) string {
	for i, a := range args {
		if a == k && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func (s *simulator) Run(ctx context.Context, q Request) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	a := s.app
	c := a.Config
	l := a.Lock
	switch q.Tool {
	case "helm":
		if q.Args[0] == "version" {
			return []byte("v" + l.Helm + "+test"), nil
		}
		return []byte("kind: CustomResourceDefinition\nmetadata:\n  name: applications.argoproj.io\n---\nkind: Deployment\nimage: " + l.ArgoImage + "\nredis-image: " + l.RedisImage + "\n"), nil
	case "git":
		switch q.Args[0] {
		case "status":
			return nil, nil
		case "rev-parse":
			return []byte(strings.Repeat("a", 40)), nil
		case "ls-remote":
			return []byte(strings.Repeat("a", 40) + "\trefs/heads/main\n"), nil
		}
	case "docker":
		switch q.Args[0] {
		case "context":
			home, _ := os.UserHomeDir()
			return []byte("unix://" + filepath.Join(home, ".orbstack/run/docker.sock")), nil
		case "image":
			if q.Args[1] == "save" {
				return nil, os.WriteFile(argValue(q.Args, "--output"), []byte("archive"), 0600)
			}
			if argValue(q.Args, "--format") == "{{.Id}}" {
				return []byte("sha256:fixture-node"), nil
			}
			if argValue(q.Args, "--format") == "{{.Os}}/{{.Architecture}}" {
				return []byte("linux/arm64"), nil
			}
			return []byte("[]"), nil
		case "inspect":
			return []byte("sha256:fixture-node true"), nil
		case "exec":
			joined := strings.Join(q.Args, " ")
			if strings.Contains(joined, "images import") {
				s.effects = append(s.effects, "image:load")
				return nil, nil
			}
			if strings.Contains(joined, "images list") {
				for _, image := range a.lockedImages() {
					_, digest, _ := strings.Cut(image, "@")
					if strings.Contains(joined, "target.digest=="+digest) {
						return []byte(strings.Join(a.lockedImages(), "\n")), nil
					}
				}
			}
		}
	case "kind":
		switch q.Args[0] {
		case "version":
			return []byte("kind v" + l.Kind + " go1.27.1"), nil
		case "get":
			if q.Args[1] == "clusters" {
				if s.exists {
					return []byte(c.Cluster), nil
				}
				return nil, nil
			}
			if c.Schema == 3 {
				return []byte(c.Cluster + "-worker2\n" + c.Cluster + "-control-plane\n" + c.Cluster + "-worker3\n" + c.Cluster + "-worker"), nil
			}
			return []byte(c.Cluster + "-control-plane"), nil
		case "create":
			s.effects = append(s.effects, "cluster:create")
			s.exists = true
			return nil, os.WriteFile(argValue(q.Args, "--kubeconfig"), []byte("fixture-kubeconfig"), 0600)
		}
	case "kubectl":
		if q.Args[0] == "version" {
			return []byte(`{"clientVersion":{"gitVersion":"v` + l.Kubectl + `"}}`), nil
		}
		args := q.Args[5:]
		if len(args) == 0 {
			return nil, errors.New("missing kubectl verb")
		}
		switch args[0] {
		case "get":
			if args[1] == "-f" {
				items := []Object{}
				for _, want := range s.requestSeed(q.Input) {
					meta := want["metadata"].(Object)
					ns, _ := meta["namespace"].(string)
					if obj := s.objects[key(want["kind"].(string), ns, meta["name"].(string))]; obj != nil {
						items = append(items, obj)
					}
				}
				return jsonBytes(Object{"kind": "List", "items": items}), nil
			}
			if args[1] == "nodes" {
				if c.Schema == 3 {
					return s.fourNodes(), nil
				}
				if a.development != nil && s.objects[key("DaemonSet", "kube-system", "cilium")] == nil {
					return []byte(`{"items":[{"metadata":{"name":"` + c.Cluster + `-control-plane"},"status":{"nodeInfo":{"architecture":"arm64","operatingSystem":"linux","kubeletVersion":"v` + l.Kubernetes + `"},"conditions":[{"type":"Ready","status":"False"}]}}]}`), nil
				}
				return []byte(`{"items":[{"metadata":{"name":"` + c.Cluster + `-control-plane"},"status":{"nodeInfo":{"architecture":"arm64","operatingSystem":"linux","kubeletVersion":"v` + l.Kubernetes + `"},"conditions":[{"type":"Ready","status":"True"}]}}]}`), nil
			}
			kind := args[1]
			if kind == "crd" {
				kind = "CustomResourceDefinition"
			}
			name := args[2]
			if name == s.failRead {
				return nil, errors.New("Forbidden")
			}
			o := s.objects[key(kind, argValue(args, "-n"), name)]
			if o == nil {
				return nil, nil
			}
			if argValue(args, "-o") == "name" {
				return []byte("secret/" + name), nil
			}
			return jsonBytes(o), nil
		case "create":
			if args[1] == "--dry-run=client" {
				return jsonBytes(Object{"kind": "List", "items": s.requestSeed(q.Input)}), nil
			}
			var o Object
			if e := decode(q.Input, &o); e != nil {
				return nil, e
			}
			// Convert nested metadata to the concrete map alias used by fixture helpers.
			o["metadata"] = Object(o["metadata"].(map[string]any))
			name := o["metadata"].(Object)["name"].(string)
			if name == s.failCreate {
				return nil, errors.New("injected create failure")
			}
			kind := o["kind"].(string)
			ns, _ := o["metadata"].(Object)["namespace"].(string)
			if s.objects[key(kind, ns, name)] != nil {
				return nil, errors.New("AlreadyExists")
			}
			s.effects = append(s.effects, "create:"+name)
			s.put(o)
			if name == "atlas-refactor-root" && s.converge {
				s.gitops()
			}
			if name == "atlas-refactor-root" && s.cancelAfterRoot != nil {
				s.cancelAfterRoot()
			}
			return nil, nil
		case "apply":
			if a.development != nil && strings.Contains(string(q.Input), `"name": "cilium-config"`) {
				s.effects = append(s.effects, "cilium:apply")
				for _, o := range ciliumFixtureObjects() {
					s.put(o)
				}
				return nil, nil
			}
			s.effects = append(s.effects, "seed:apply")
			s.put(object("CustomResourceDefinition", "", "applications.argoproj.io"))
			return nil, nil
		case "wait", "rollout":
			return nil, nil
		}
	}
	return nil, fmt.Errorf("unmodeled command: %s %v", q.Tool, q.Args)
}
func (s *simulator) gitops() {
	if s.app.development != nil {
		s.developmentGitops()
		return
	}
	a := s.app
	root := s.objects[key("Application", "argocd", "atlas-refactor-root")]
	root["status"] = Object{"sync": Object{"status": "Synced", "revision": strings.Repeat("a", 40)}, "health": Object{"status": "Healthy"}}
	self := a.application("argocd-self", "platform-project", a.Config.GitOpsPath+"/platform/argocd", "0")
	self["status"] = Object{"sync": Object{"status": "Synced", "revision": strings.Repeat("a", 40)}, "health": Object{"status": "Healthy"}, "resources": []Object{{"group": "", "kind": "ConfigMap", "namespace": "argocd", "name": "atlas-refactor-adoption-signal"}}}
	s.put(self)
	for _, child := range []struct{ name, project, path, wave string }{{"project-bootstrap", "atlas-bootstrap", "/projects", "-20"}, {"platform-control", "platform-project", "/platform/applications", "-10"}} {
		obj := a.application(child.name, child.project, a.Config.GitOpsPath+child.path, child.wave)
		obj["status"] = Object{"sync": Object{"status": "Synced", "revision": strings.Repeat("a", 40)}, "health": Object{"status": "Healthy"}}
		s.put(obj)
	}
	for _, obj := range s.seedObjects() {
		var projected Live
		_ = decode(jsonBytes(obj), &projected)
		meta := obj["metadata"].(Object)
		if projected.Metadata.Namespace == "" {
			projected.Metadata.Namespace = "argocd"
		}
		meta["annotations"] = map[string]string{"argocd.argoproj.io/tracking-id": "argocd-self:" + seedKey(&projected)}
		meta["managedFields"] = []Object{{"manager": "argocd-controller", "operation": "Apply", "fieldsV1": Object{"f:spec": Object{}}}}
		if obj["kind"] == "CustomResourceDefinition" {
			delete(meta, "annotations")
		}
		s.put(obj)
	}
	resources := []Object{{"group": "", "kind": "ConfigMap", "namespace": "argocd", "name": "atlas-refactor-adoption-signal"}}
	results := []Object{}
	for _, obj := range s.seedObjects() {
		if obj["kind"] != "CustomResourceDefinition" {
			continue
		}
		name := obj["metadata"].(Object)["name"]
		resources = append(resources, Object{"group": "apiextensions.k8s.io", "version": "v1", "kind": "CustomResourceDefinition", "name": name, "status": "Synced"})
		results = append(results, Object{"group": "apiextensions.k8s.io", "version": "v1", "kind": "CustomResourceDefinition", "name": name, "namespace": "argocd", "status": "Synced", "syncPhase": "Sync"})
	}
	self["status"].(Object)["resources"] = resources
	self["status"].(Object)["operationState"] = Object{"phase": "Succeeded", "syncResult": Object{"revision": strings.Repeat("a", 40), "resources": results}}
	signal := configMap("argocd", "atlas-refactor-adoption-signal", map[string]string{"fingerprint": a.fingerprint()})
	signal["metadata"].(Object)["annotations"] = map[string]string{"argocd.argoproj.io/tracking-id": "argocd-self:/ConfigMap:argocd/atlas-refactor-adoption-signal"}
	s.put(signal)
}

func (s *simulator) seedObjects() []Object {
	cm := object("ConfigMap", "argocd", "argocd-cm")
	cm["data"] = map[string]string{"application.resourceTrackingMethod": "annotation"}
	deployment := object("Deployment", "argocd", "atlas-refactor-argocd-server")
	deployment["apiVersion"] = "apps/v1"
	deployment["spec"] = Object{"replicas": 1}
	crd := object("CustomResourceDefinition", "", "applications.argoproj.io")
	crd["apiVersion"] = "apiextensions.k8s.io/v1"
	crd["spec"] = Object{"group": "argoproj.io"}
	role := object("ClusterRole", "", "atlas-refactor-argocd-application-controller")
	role["apiVersion"] = "rbac.authorization.k8s.io/v1"
	return []Object{cm, deployment, crd, role}
}
func fixture(t *testing.T) (*App, *simulator) {
	t.Helper()
	return fixtureFrom(t, filepath.Join("..", ".."))
}

func fixtureFrom(t *testing.T, source string) (*App, *simulator) {
	t.Helper()
	root := t.TempDir()
	b, e := os.ReadFile(filepath.Join(source, "versions.lock.json"))
	if e != nil {
		t.Fatal(e)
	}
	var l Lock
	if e := strictJSON(b, &l); e != nil {
		t.Fatal(e)
	}
	if e := l.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"versions.lock.json", "assets/argocd-values.yaml", "assets/argocd-cm.yaml", l.Chart} {
		input := filepath.Join(source, p)
		if p == l.Chart {
			input = filepath.Join("..", "..", p)
		}
		b, e := os.ReadFile(input)
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(root, p)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := Config{1, "atlas-refactor-test", "https://example.com/owner/atlas-refactor.git", "main", "gitops/test", "orbstack", 30}
	a := &App{Root: root, Config: c, Lock: l}
	s := &simulator{app: a, converge: true, objects: map[string]Object{}}
	a.Runner = s
	files, e := a.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = WriteFiles(root, c.GitOpsPath, files); e != nil {
		t.Fatal(e)
	}
	return a, s
}
func apply(t *testing.T, a *App) {
	t.Helper()
	if e := a.Apply(context.Background(), a.Config.Cluster, true); e != nil {
		t.Fatal(e)
	}
}

func TestFullBootstrapAndIdempotentAdoption(t *testing.T) {
	a, s := fixture(t)
	apply(t, a)
	if r := a.Status(context.Background()); r.State != Adopted {
		t.Fatalf("%+v", r)
	}
	want := []string{"cluster:create", "create:atlas-refactor-identity", "image:load", "image:load", "create:argocd", "create:argocd-secret", "seed:apply", "create:atlas-bootstrap", "create:atlas-refactor-handoff", "create:atlas-refactor-root", "create:atlas-refactor-receipt"}
	if !reflect.DeepEqual(s.effects, want) {
		t.Fatalf("unexpected effect order: %v", s.effects)
	}
	n := len(s.effects)
	apply(t, a)
	if len(s.effects) != n {
		t.Fatal("adopted retry mutated the target")
	}
}

func TestAdoptedDamageNeverRestoresSeed(t *testing.T) {
	cases := []struct {
		name, kind, ns, obj string
		want                State
	}{{"missing root", "Application", "argocd", "atlas-refactor-root", Degraded}, {"missing self", "Application", "argocd", "argocd-self", Degraded}, {"missing signal", "ConfigMap", "argocd", "atlas-refactor-adoption-signal", Degraded}, {"missing CRD", "CustomResourceDefinition", "", "applications.argoproj.io", Degraded}, {"missing identity", "ConfigMap", "kube-system", "atlas-refactor-identity", Drifted}, {"missing project", "AppProject", "argocd", "atlas-bootstrap", Drifted}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, s := fixture(t)
			apply(t, a)
			delete(s.objects, key(tc.kind, tc.ns, tc.obj))
			n := len(s.effects)
			if r := a.Status(context.Background()); r.State != tc.want {
				t.Fatalf("%+v", r)
			}
			if e := a.Apply(context.Background(), a.Config.Cluster, true); e == nil {
				t.Fatal("expected denial")
			}
			if len(s.effects) != n {
				t.Fatal("damaged adoption caused mutation")
			}
		})
	}
}

func TestFailureBeforeRootLeavesDurableLatch(t *testing.T) {
	a, s := fixture(t)
	s.failCreate = "atlas-refactor-root"
	if e := a.Apply(context.Background(), a.Config.Cluster, true); e == nil {
		t.Fatal("expected failure")
	}
	n := len(s.effects)
	s.failCreate = ""
	if e := a.Apply(context.Background(), a.Config.Cluster, true); e == nil {
		t.Fatal("missing Root cannot be silently repaired")
	}
	if len(s.effects) != n {
		t.Fatal("retry after uncertain handoff mutated")
	}
}

func TestReceiptRetryDoesNotRepeatBootstrap(t *testing.T) {
	a, s := fixture(t)
	s.failCreate = "atlas-refactor-receipt"
	if e := a.Apply(context.Background(), a.Config.Cluster, true); e == nil {
		t.Fatal("expected failure")
	}
	n := len(s.effects)
	s.failCreate = ""
	apply(t, a)
	if !reflect.DeepEqual(s.effects[n:], []string{"create:atlas-refactor-receipt"}) {
		t.Fatalf("%v", s.effects[n:])
	}
}

func TestUnavailableIsNotAbsence(t *testing.T) {
	a, s := fixture(t)
	apply(t, a)
	s.failRead = "atlas-refactor-identity"
	n := len(s.effects)
	if r := a.Status(context.Background()); r.State != Unavailable {
		t.Fatalf("%+v", r)
	}
	if e := a.Apply(context.Background(), a.Config.Cluster, true); e == nil {
		t.Fatal("expected refusal")
	}
	if len(s.effects) != n {
		t.Fatal("Forbidden produced mutation")
	}
}

func TestRootDriftAndUIDContradictions(t *testing.T) {
	for _, mode := range []string{"root-spec", "signal-uid", "root-finalizer", "self-spec"} {
		t.Run(mode, func(t *testing.T) {
			a, s := fixture(t)
			apply(t, a)
			root := s.objects[key("Application", "argocd", "atlas-refactor-root")]
			switch mode {
			case "root-spec":
				root["spec"].(map[string]any)["project"] = "default"
			case "signal-uid":
				s.objects[key("ConfigMap", "argocd", "atlas-refactor-adoption-signal")]["metadata"].(Object)["uid"] = "replacement"
			case "root-finalizer":
				root["metadata"].(Object)["finalizers"] = []string{"resources-finalizer.argocd.argoproj.io"}
			case "self-spec":
				s.objects[key("Application", "argocd", "argocd-self")]["spec"].(Object)["project"] = "default"
			}
			n := len(s.effects)
			if r := a.Status(context.Background()); r.State != Drifted {
				t.Fatalf("%+v", r)
			}
			_ = a.Apply(context.Background(), a.Config.Cluster, true)
			if len(s.effects) != n {
				t.Fatal("drift caused writes")
			}
		})
	}
}

func TestApprovalAndLockPreventEffects(t *testing.T) {
	a, s := fixture(t)
	for _, approved := range []string{"", "atlas-test", "atlas-refactor-test-other"} {
		if e := a.Apply(context.Background(), approved, true); e == nil {
			t.Fatal("approval accepted")
		}
	}
	if e := a.Apply(context.Background(), a.Config.Cluster, false); e == nil {
		t.Fatal("Tier-0 gate omitted")
	}
	release, e := a.acquire()
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = a.Apply(context.Background(), a.Config.Cluster, true); e == nil {
		t.Fatal("lock ignored")
	}
	if len(s.effects) != 0 {
		t.Fatal("denied action had effects")
	}
}

func TestInputAndArtifactFailures(t *testing.T) {
	for _, data := range []string{`{"schema":1,"schema":1}`, `{"schema":1,"unknown":1}`, `{"schema":1} {}`, `{"assets":{"x":"a","x":"b"}}`} {
		var c Config
		if e := strictJSON([]byte(data), &c); e == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	a, _ := fixture(t)
	c := a.Config
	c.RepositoryURL = "https://user:secret@example.com/a.git"
	if c.Validate() == nil {
		t.Fatal("embedded credential accepted")
	}
	c = a.Config
	c.Cluster = "atlas-test"
	if c.Validate() == nil {
		t.Fatal("old Atlas cluster accepted")
	}
	if e := os.WriteFile(filepath.Join(a.Root, a.Lock.Chart), []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Render(context.Background()); e == nil {
		t.Fatal("tampered chart accepted")
	}
}

func TestPathsAndKubeconfigFailClosed(t *testing.T) {
	a, s := fixture(t)
	apply(t, a)
	if _, e := safePath(a.Root, "../escape"); e == nil {
		t.Fatal("path escape accepted")
	}
	if e := os.Symlink(t.TempDir(), filepath.Join(a.Root, "linked")); e != nil {
		t.Fatal(e)
	}
	if e := WriteFiles(a.Root, "linked", map[string][]byte{"file": []byte("no")}); e == nil {
		t.Fatal("symlink output accepted")
	}
	if e := os.WriteFile(filepath.Join(a.Root, ".state/kubeconfig"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	n := len(s.effects)
	if r := a.Status(context.Background()); r.State != Unavailable {
		t.Fatalf("%+v", r)
	}
	_ = a.Apply(context.Background(), a.Config.Cluster, true)
	if n != len(s.effects) {
		t.Fatal("modified kubeconfig caused effects")
	}
}

func TestDeterministicRenderAndBoundaries(t *testing.T) {
	a, _ := fixture(t)
	x, e := a.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	y, e := a.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatal("render not deterministic")
	}
	if string(x["bootstrap/seed.yaml"]) != string(x["platform/argocd/seed.yaml"]) {
		t.Fatal("Seed/self mismatch")
	}
	var root Live
	if e = decode(x["bootstrap/root.json"], &root); e != nil {
		t.Fatal(e)
	}
	if len(root.Metadata.Finalizers) != 0 {
		t.Fatal("cascading Root finalizer")
	}
	if _, ok := x["root/root.json"]; ok {
		t.Fatal("External Root self-managed")
	}
}

func TestProcessRejectsAmbientTarget(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://foreign:2375")
	_, e := (ExecRunner{Root: t.TempDir()}).Run(context.Background(), Request{Tool: "docker", Args: []string{"version"}})
	if e == nil || !strings.Contains(e.Error(), "DOCKER_HOST") {
		t.Fatalf("%v", e)
	}
}

func TestLockedHelmRender(t *testing.T) {
	helm := os.Getenv("ATLAS_TEST_HELM")
	if helm == "" {
		t.Skip("set ATLAS_TEST_HELM to the preinstalled locked Helm binary for real render verification")
	}
	resolved, e := exec.LookPath(helm)
	if e != nil {
		t.Fatal(e)
	}
	resolved, e = filepath.Abs(resolved)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := fixture(t)
	dir := t.TempDir()
	if e := os.Symlink(resolved, filepath.Join(dir, "helm")); e != nil {
		t.Fatal(e)
	}
	a.Runner = ExecRunner{Root: a.Root, ToolDir: dir, DockerContext: "orbstack"}
	x, e := a.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	y, e := a.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatal("real Helm render changed between runs")
	}
}

func FuzzStrictJSON(f *testing.F) {
	for _, s := range []string{`{}`, `{"schema":1}`, `{"schema":1,"schema":2}`, `null`, `[]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { var c Config; _ = strictJSON([]byte(s), &c) })
}
