package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"atlas-refactor/internal/platformnames"
)

func TestDataCapabilitySelectionIsOptInAndCredentialGated(t *testing.T) {
	p := candidate(t)
	for _, name := range []string{"postgresql", "redis", "kafka", "flink", "loki", "log-collection", "data-observability"} {
		for _, active := range p.Capabilities.Active {
			if active == name {
				t.Fatalf("%s silently enabled", name)
			}
		}
	}
	for _, image := range p.Lock.Images {
		for _, repo := range []string{"cloudnative-pg/", "strimzi/", "library/flink:", "grafana/loki:", "grafana/alloy:"} {
			if strings.Contains(image, repo) {
				t.Fatalf("inactive image enters installation: %s", image)
			}
		}
	}
	plan, err := p.CapabilityPlan([]string{"postgresql", "redis", "kafka", "flink", "data-observability"})
	if err != nil {
		t.Fatal(err)
	}
	if plan["readyToEnable"] != false {
		t.Fatal("missing credentials accepted")
	}
	missing := plan["missingSealedSecrets"].([]string)
	for _, key := range []string{"postgresql/postgresql-app/username", "postgresql/postgresql-app/password", "flink/flink-webhook-password/password", "redis/redis-auth/password", "redis/redis-auth/users.acl"} {
		found := false
		for _, value := range missing {
			found = found || value == key
		}
		if !found {
			t.Fatal("missing gate", key, missing)
		}
	}
	for _, ns := range []string{"postgresql", "redis", "kafka", "flink", "logging"} {
		if !platformnames.Reserved(ns) {
			t.Fatal("business may shadow platform", ns)
		}
	}
}

func TestDataCapabilityAuthorityAndRuntimeBoundaries(t *testing.T) {
	p := candidate(t)
	names, err := p.ResolveCapabilities([]string{"postgresql", "redis", "kafka", "flink", "data-observability"})
	if err != nil {
		t.Fatal(err)
	}
	files, err := p.CapabilityActivation(names)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatal("unexpected authority write", files)
	}
	projects, err := decodeObjects(files[platformProjects])
	if err != nil {
		t.Fatal(err)
	}
	original := resourceFile(t, capabilityDir+"/core-projects.json")
	if !bytes.Equal(encoded(projects[1:]), encoded(original[1:])) {
		t.Fatal("tenant permissions changed")
	}
	if allowed(field(projects[0], "spec", "namespaceResourceWhitelist"), "", "Secret") {
		t.Fatal("plaintext Secret authority")
	}
	for _, name := range []string{"postgresql", "redis", "kafka", "flink", "loki", "log-collection"} {
		objects, err := p.capabilityObjects(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objects {
			if err := checkObject(o); err != nil {
				t.Fatal(name, err)
			}
			switch o["kind"] {
			case "ClusterRole", "ClusterRoleBinding", "Secret", "HTTPRoute", "Gateway":
				t.Fatalf("unexpected data authority/exposure: %s %s", name, o["kind"])
			case "Service":
				if typ := field(o, "spec", "type"); typ != nil && typ != "ClusterIP" {
					t.Fatal("public data listener", name)
				}
			case "Role":
				if name != "log-collection" {
					t.Fatal("data workload has unexpected authored API privileges", name)
				}
				for _, rule := range slice(o["rules"]) {
					for _, resource := range slice(field(mapping(rule), "resources")) {
						if resource != "pods" && resource != "pods/log" {
							t.Fatal("log scope expansion", resource)
						}
					}
					for _, verb := range slice(field(mapping(rule), "verbs")) {
						if verb != "get" && verb != "list" && verb != "watch" {
							t.Fatal("log mutation authority", verb)
						}
					}
				}
			case "StatefulSet", "Deployment":
				ps := mapping(field(o, "spec", "template", "spec"))
				if name != "log-collection" && ps["automountServiceAccountToken"] != false {
					t.Fatal("unexpected API token", name)
				}
				if field(ps, "securityContext", "runAsNonRoot") != true {
					t.Fatal("root process", name)
				}
				for _, raw := range append(slice(ps["containers"]), slice(ps["initContainers"])...) {
					c := mapping(raw)
					if field(c, "securityContext", "readOnlyRootFilesystem") != true || field(c, "securityContext", "allowPrivilegeEscalation") != false {
						t.Fatal("weak container security", name)
					}
					if field(c, "resources", "requests", "memory") == nil || field(c, "resources", "limits", "memory") == nil {
						t.Fatal("unbounded memory", name)
					}
				}
			}
		}
	}
}

func TestDataFoundationDeniesTrafficBeforeConsumersExist(t *testing.T) {
	p := candidate(t)
	for _, ns := range []string{"postgresql", "redis", "kafka", "flink", "logging"} {
		objects, err := p.capabilityObjects(ns + "-foundation")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, o := range objects {
			if o["kind"] == "NetworkPolicy" && metadata(o)["name"] == "default-deny" {
				found = len(mapping(field(o, "spec", "podSelector"))) == 0 && len(slice(field(o, "spec", "ingress"))) == 0 && len(slice(field(o, "spec", "egress"))) == 0 && contains(field(o, "spec", "policyTypes"), "Ingress") && contains(field(o, "spec", "policyTypes"), "Egress")
			}
		}
		if !found {
			t.Fatal("missing deny baseline", ns)
		}
	}
}

func TestDataCredentialProfileRejectsUnknownBeforeWriting(t *testing.T) {
	p := candidate(t)
	p.Root = t.TempDir()
	if err := p.PrepareCredentialProfile(context.Background(), "", "", "../../escape"); err == nil {
		t.Fatal("accepted unknown profile")
	}
	entries, err := os.ReadDir(p.Root)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestDataCredentialsCannotBypassStrictSealing(t *testing.T) {
	p := unsealedCandidate(t)
	path := filepath.Join(p.Root, capabilityDir, "resources/data-credentials.json")
	for _, input := range []string{
		`{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"redis-auth","namespace":"redis"}}]}`,
		`{"apiVersion":"v1","kind":"List","items":[{"apiVersion":"bitnami.com/v1alpha1","kind":"SealedSecret","metadata":{"name":"redis-auth","namespace":"redis","annotations":{"sealedsecrets.bitnami.com/namespace-wide":"true"}},"spec":{"template":{"metadata":{"name":"redis-auth","namespace":"redis"}}}}]}`,
	} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.missingCapabilitySecrets([]string{"redis"}); err == nil {
			t.Fatal("unsafe data credential accepted")
		}
	}
}

func TestNewDataCRDsAreDefaultStable(t *testing.T) {
	p := candidate(t)
	names, err := p.ResolveCapabilities([]string{"postgresql", "redis", "kafka", "flink", "data-observability"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := p.capabilityResourceModel(names)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"data-pki", "data-observability", "postgresql", "kafka", "flink", "flink-operator"} {
		objects, err := p.capabilityObjects(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objects {
			if err := model.ValidateCRDDefaultStability(nil, o); err != nil {
				t.Errorf("%s: %v", identity(o), err)
			}
		}
	}
}

func TestRedisProbeRejectsAuthErrors(t *testing.T) {
	p := candidate(t)
	objects, e := p.capabilityObjects("redis")
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range objects {
		if o["kind"] != "StatefulSet" {
			continue
		}
		c := mapping(slice(field(o, "spec", "template", "spec", "containers"))[0])
		for _, probe := range []string{"startupProbe", "readinessProbe", "livenessProbe"} {
			if !contains(field(c, probe, "exec", "command"), "-e") {
				t.Fatal("redis auth error may return success", probe)
			}
			args := slice(field(c, probe, "exec", "command"))
			// redis-cli stops parsing options at the command. Trailing --sni
			// becomes a PING argument and makes every otherwise valid probe fail.
			if len(args) < 4 || args[len(args)-1] != "ping" ||
				args[len(args)-3] != "--sni" || args[len(args)-2] != "redis.redis.svc.cluster.local" {
				t.Fatal("Redis TLS options must precede the PING command", probe, args)
			}
		}
	}
}

func TestCNPGSpecMatchesAdmittedReleaseDefaults(t *testing.T) {
	// Spec captured after actual CNPG 1.30.1 admission, stripped of all
	// metadata/status. Structural CRD validation alone cannot predict these
	// webhook defaults. Keep them explicit rather than ignoring live extras.
	fixture, err := os.ReadFile(filepath.Join(testRoot(t), "internal/platform/testdata/cnpg-1.30.1-admitted-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected Object
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	p := candidate(t)
	objects, err := p.capabilityObjects("postgresql")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o["kind"] == "Cluster" {
			got, _ := json.Marshal(o["spec"])
			want, _ := json.Marshal(expected)
			if !bytes.Equal(got, want) {
				t.Fatal("CNPG desired spec differs from the locked admission representation")
			}
			return
		}
	}
	t.Fatal("CNPG Cluster not rendered")
}

func TestOperatorHealthRequiresCurrentGeneration(t *testing.T) {
	lua := os.Getenv("ATLAS_TEST_LUA")
	if lua == "" {
		t.Skip("set ATLAS_TEST_LUA")
	}
	script, _ := json.Marshal(filepath.Join(testRoot(t), inputDir, "health/data-operators.lua"))
	for _, kind := range []string{"Kafka", "KafkaUser", "FlinkDeployment"} {
		api := "kafka.strimzi.io/v1"
		if kind == "FlinkDeployment" {
			api = "flink.apache.org/v1beta1"
		}
		for _, state := range []string{"current", "stale", "missing", "failed"} {
			o := Object{"apiVersion": api, "kind": kind, "metadata": Object{"generation": 2}}
			status := Object{"observedGeneration": 2, "conditions": []any{Object{"type": "Ready", "status": "True", "observedGeneration": 2}}, "lifecycleState": "STABLE", "jobManagerDeploymentStatus": "READY"}
			o["status"] = status
			want := "Healthy"
			switch state {
			case "stale":
				status["observedGeneration"] = 1
				mapping(slice(status["conditions"])[0])["observedGeneration"] = 1
				want = "Progressing"
			case "missing":
				delete(o, "status")
				want = "Progressing"
			case "failed":
				mapping(slice(status["conditions"])[0])["status"] = "False"
				status["lifecycleState"] = "FAILED"
				want = "Degraded"
			}
			src := "obj=" + luaLiteral(o) + "\nlocal h=assert(loadfile(" + string(script) + "))()\nassert(h.status == '" + want + "',h.status)\n"
			cmd := exec.Command(lua, "-")
			cmd.Stdin = strings.NewReader(src)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("%s/%s: %s %v", kind, state, b, e)
			}
		}
	}
}

func TestCNPGHealthMatchesLockedReleaseReadiness(t *testing.T) {
	lua := os.Getenv("ATLAS_TEST_LUA")
	if lua == "" {
		t.Skip("set ATLAS_TEST_LUA")
	}
	script, _ := json.Marshal(filepath.Join(testRoot(t), inputDir, "health/data-operators.lua"))
	// v1.30.1 SetClusterReadyCondition omits observedGeneration. Do not
	// invent a generation guarantee, or wait forever for an absent field.
	base := Object{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "metadata": Object{"generation": 2},
		"spec": Object{"instances": 1, "imageName": "locked-image"},
		"status": Object{"phase": "Cluster in healthy state", "instances": 1, "readyInstances": 1,
			"image": "locked-image", "currentPrimary": "postgresql-1", "targetPrimary": "postgresql-1",
			"conditions": []any{Object{"type": "Ready", "status": "True"}}},
	}
	for _, tc := range []struct {
		name, want string
		change     func(Object)
	}{
		{"release readiness", "Healthy", func(Object) {}},
		{"zero generation", "Healthy", func(o Object) { mapping(slice(field(o, "status", "conditions"))[0])["observedGeneration"] = 0 }},
		{"stale generation", "Progressing", func(o Object) { mapping(slice(field(o, "status", "conditions"))[0])["observedGeneration"] = 1 }},
		{"missing status", "Progressing", func(o Object) { delete(o, "status") }},
		{"missing condition", "Progressing", func(o Object) { delete(mapping(o["status"]), "conditions") }},
		{"duplicate condition", "Progressing", func(o Object) {
			mapping(o["status"])["conditions"] = append(slice(field(o, "status", "conditions")), Object{"type": "Ready", "status": "True"})
		}},
		{"not ready", "Degraded", func(o Object) { mapping(slice(field(o, "status", "conditions"))[0])["status"] = "False" }},
		{"first primary initializing", "Progressing", func(o Object) {
			mapping(o["status"])["phase"] = "Setting up primary"
			mapping(o["status"])["conditions"] = []any{Object{"type": "Ready", "status": "False", "reason": "ClusterIsNotReady"}}
		}},
		{"waiting for instance", "Progressing", func(o Object) {
			mapping(o["status"])["phase"] = "Waiting for the instances to become active"
			mapping(o["status"])["conditions"] = []any{Object{"type": "Ready", "status": "False", "reason": "ClusterIsNotReady"}}
		}},
		{"startup with different failure", "Degraded", func(o Object) {
			mapping(o["status"])["phase"] = "Setting up primary"
			mapping(o["status"])["conditions"] = []any{Object{"type": "Ready", "status": "False", "reason": "InvalidConfiguration"}}
		}},
		{"unrecoverable", "Degraded", func(o Object) {
			mapping(o["status"])["phase"] = "Cluster is unrecoverable and needs manual intervention"
			mapping(o["status"])["conditions"] = []any{Object{"type": "Ready", "status": "False", "reason": "ClusterIsNotReady"}}
		}},
		{"unknown phase not ready", "Degraded", func(o Object) {
			mapping(o["status"])["phase"] = "Unexpected upstream state"
			mapping(o["status"])["conditions"] = []any{Object{"type": "Ready", "status": "False", "reason": "ClusterIsNotReady"}}
		}},
		{"unknown ready", "Progressing", func(o Object) { mapping(slice(field(o, "status", "conditions"))[0])["status"] = "Unknown" }},
		{"rolling phase", "Progressing", func(o Object) { mapping(o["status"])["phase"] = "Rolling update in progress" }},
		{"changed image", "Progressing", func(o Object) { mapping(o["spec"])["imageName"] = "new-locked-image" }},
		{"changed instances", "Progressing", func(o Object) { mapping(o["spec"])["instances"] = 2 }},
		{"unready instance", "Progressing", func(o Object) { mapping(o["status"])["readyInstances"] = 0 }},
		{"primary changing", "Progressing", func(o Object) { mapping(o["status"])["targetPrimary"] = "postgresql-2" }},
		{"missing primary", "Progressing", func(o Object) { delete(mapping(o["status"]), "currentPrimary") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := clone(base)
			tc.change(o)
			src := "obj=" + luaLiteral(o) + "\nlocal h=assert(loadfile(" + string(script) + "))()\nassert(h.status == '" + tc.want + "',h.status)\n"
			cmd := exec.Command(lua, "-")
			cmd.Stdin = strings.NewReader(src)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s %v", b, err)
			}
		})
	}
}

func TestOperatorsOwnChildrenWithinNamespace(t *testing.T) {
	p := candidate(t)
	for _, tc := range []struct{ ns, kind string }{{"postgresql", "Cluster"}, {"kafka", "Kafka"}, {"flink", "FlinkDeployment"}} {
		objects, e := p.capabilityObjects(tc.ns)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, o := range objects {
			if o["kind"] == tc.kind {
				found = true
			}
			if o["kind"] == "Deployment" || o["kind"] == "StatefulSet" {
				t.Fatal("Argo owns operator runtime children", tc.ns)
			}
		}
		if !found {
			t.Fatal("missing domain intent", tc.ns)
		}
		objects, e = p.capabilityObjects(tc.ns + "-operator")
		if e != nil {
			t.Fatal(e)
		}
		roles := map[string]Object{}
		for _, o := range objects {
			if o["kind"] == "ClusterRole" {
				roles[str(metadata(o)["name"])] = o
			}
			if o["kind"] == "RoleBinding" {
				ns := str(metadata(o)["namespace"])
				if ns != "" && ns != tc.ns {
					t.Fatal("cross namespace binding", identity(o))
				}
			}
		}
		for _, o := range objects {
			if o["kind"] != "ClusterRoleBinding" {
				continue
			}
			role := roles[str(field(o, "roleRef", "name"))]
			if role == nil {
				t.Fatal("unreviewed global role", identity(o))
			}
			for _, raw := range slice(role["rules"]) {
				r := mapping(raw)
				for _, resource := range slice(r["resources"]) {
					switch resource {
					case "nodes", "storageclasses", "clusterimagecatalogs":
						for _, verb := range slice(r["verbs"]) {
							if verb != "get" && verb != "list" && verb != "watch" {
								t.Fatal("global mutation", r)
							}
						}
					case "mutatingwebhookconfigurations", "validatingwebhookconfigurations":
						if len(slice(r["resourceNames"])) != 2 {
							t.Fatal("unbounded webhook writes", r)
						}
					default:
						t.Fatal("global runtime authority", tc.ns, r)
					}
				}
			}
		}
	}
}

func TestCNPGOperandImagesMustBeLocked(t *testing.T) {
	p := candidate(t)
	p.Lock = p.Capabilities.Lock
	for _, image := range []string{"ghcr.io/cloudnative-pg/postgresql:18.6", "ghcr.io/cloudnative-pg/postgresql:latest"} {
		if p.images(Object{"spec": Object{"imageName": image}}) == nil {
			t.Fatal("unlocked operator operand accepted", image)
		}
	}
}

func TestFlinkFixedSessionHasNoWorkloadAPIAuthority(t *testing.T) {
	p := candidate(t)
	objects, err := p.capabilityObjects("flink")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o["kind"] == "FlinkDeployment" && (field(o, "spec", "mode") != "standalone" || field(o, "spec", "podTemplate", "spec", "automountServiceAccountToken") != false) {
			t.Fatal("fixed session silently acquired native runtime API authority")
		}
	}
	objects, err = p.capabilityObjects("flink-operator")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o["kind"] == "RoleBinding" {
			for _, raw := range slice(o["subjects"]) {
				if field(mapping(raw), "name") == "flink" {
					t.Fatal("runtime SA acquired a role")
				}
			}
		}
	}
}

func TestOperatorRuntimeToleratesDedicatedDataNode(t *testing.T) {
	p := candidate(t)
	// D1's data worker has node-role.local/data=true:NoSchedule. A node
	// selector alone cannot place a new operator-managed runtime there.
	for _, tc := range []struct {
		component, kind string
		podConfig       []string
	}{
		{"postgresql", "Cluster", []string{"spec", "affinity"}},
		{"kafka", "KafkaNodePool", []string{"spec", "template", "pod"}},
		{"flink", "FlinkDeployment", []string{"spec", "podTemplate", "spec"}},
	} {
		objects, err := p.capabilityObjects(tc.component)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, o := range objects {
			if o["kind"] != tc.kind {
				continue
			}
			for _, raw := range slice(field(mapping(field(o, tc.podConfig...)), "tolerations")) {
				tol := mapping(raw)
				if tol["key"] == "node-role.local/data" && tol["operator"] == "Equal" && tol["value"] == "true" && tol["effect"] == "NoSchedule" {
					found = true
				}
			}
		}
		if !found {
			t.Fatal(tc.component, "cannot schedule on D1's tainted data worker")
		}
	}
}
