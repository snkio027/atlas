package managementaccess

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/snkio027/atlas/internal/platform"
	"github.com/snkio027/atlas/internal/workload"
)

func dataFixture(t *testing.T) (Files, *platform.ResourceModel) {
	t.Helper()
	files, model := fixture(t)
	p, err := platform.Load("../..", platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if err != nil {
		t.Fatal(err)
	}
	names, err := p.ResolveCapabilities(append(p.Capabilities.Active, "postgresql", "redis", "kafka", "flink", "data-observability"))
	if err != nil {
		t.Fatal(err)
	}
	delta, err := p.CapabilityActivation(names)
	if err != nil {
		t.Fatal(err)
	}
	p.Capabilities.Active = names
	model, err = p.ObservationResourceModel()
	if err != nil {
		t.Fatal(err)
	}
	return overlay(files, delta), model
}

func TestNativeDataPreservesOwnershipAuthAndInternalListeners(t *testing.T) {
	base, model := dataFixture(t)
	config := LocalDataConfig{"atlas-example-worker3.orb.local", "192.168.138.1"}
	before := platform.BundleDigest(base)
	r, err := RenderLocalData(base, model, config)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := RenderLocalData(base, model, config)
	if err != nil || !reflect.DeepEqual(r, repeated) {
		t.Fatal("non-deterministic", err)
	}
	if before != platform.BundleDigest(base) {
		t.Fatal("mutated input")
	}
	if len(r.Certificates) != 2 || len(r.Services) != 3 {
		t.Fatal("unexpected scope")
	}
	original, err := workload.InventoryOf(base, model)
	if err != nil {
		t.Fatal(err)
	}
	final, err := workload.InventoryOf(overlay(base, r.Certificates, r.Services), model)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != len(original)+4 {
		t.Fatal("unexpected new identities")
	}
	// Every historical object is preserved; only cert SAN/Redis restart/Kafka
	// external listener may change. Credential and internal endpoint bytes survive.
	changed := map[string]bool{}
	for _, old := range original {
		for _, n := range final {
			if n.Identity == old.Identity {
				if n.Owner != old.Owner || n.Path != old.Path {
					t.Fatal("ownership changed")
				}
				if n.SHA256 != old.SHA256 {
					changed[n.Identity] = true
				}
			}
		}
	}
	want := map[string]bool{"postgresql.cnpg.io/Cluster/service-postgresql/postgresql": true, "cert-manager.io/Certificate/service-redis/server-tls": true, "apps/StatefulSet/service-redis/redis": true, "kafka.strimzi.io/Kafka/service-kafka/kafka": true}
	if !reflect.DeepEqual(changed, want) {
		t.Fatal(changed)
	}
	for _, v := range []struct {
		path, ns, name string
		port           int
	}{{PostgreSQLPath, "service-postgresql", "postgresql-local", PostgreSQLPort}, {RedisPath, "service-redis", "redis-local", RedisPort}} {
		xs := decode(t, r.Services[v.path])
		svc := mustGet(t, xs, "Service", v.ns, v.name)
		if at(svc, "spec", "externalTrafficPolicy") != "Local" || at(obj(arr(at(svc, "spec", "ports"))[0]), "nodePort") != float64(v.port) {
			t.Fatal("source preservation/port contract lost")
		}
		np := mustGet(t, xs, "NetworkPolicy", v.ns, v.name)
		peer := obj(arr(at(obj(arr(at(np, "spec", "ingress"))[0]), "from"))[0])
		if !reflect.DeepEqual(peer, Object{"ipBlock": Object{"cidr": config.ClientAddress + "/32"}}) {
			t.Fatal("source scope widened")
		}
	}
	cr := mustGet(t, decode(t, r.Services[KafkaPath]), "Kafka", "service-kafka", "kafka")
	old := mustGet(t, decode(t, base[KafkaPath]), "Kafka", "service-kafka", "kafka")
	listeners := arr(at(cr, "spec", "kafka", "listeners"))
	if !reflect.DeepEqual(listeners[0], arr(at(old, "spec", "kafka", "listeners"))[0]) {
		t.Fatal("internal listener changed")
	}
	local := obj(listeners[1])
	broker := obj(arr(at(local, "configuration", "brokers"))[0])
	if at(local, "authentication", "type") != "tls" || local["tls"] != true || broker["advertisedHost"] != config.NodeHost || broker["advertisedPort"] != float64(KafkaBrokerPort) {
		t.Fatal("native metadata/TLS invalid")
	}
	if _, err := RenderLocalData(overlay(base, r.Certificates, r.Services), model, config); err == nil {
		t.Fatal("accepted duplicate enablement")
	}
	if tool := os.Getenv("ATLAS_TEST_KUBECTL"); tool != "" {
		for _, phase := range []Files{r.Certificates, r.Services} {
			base = overlay(base, phase)
			dir := filepath.Join(t.TempDir(), "tree")
			if err := WriteNew(dir, base); err != nil {
				t.Fatal(err)
			}
			for path := range phase {
				if b, err := exec.Command(tool, "kustomize", filepath.Join(dir, filepath.Dir(path))).CombinedOutput(); err != nil {
					t.Fatalf("Kustomize %s: %s %v", path, b, err)
				}
			}
		}
	}
}

func TestNativeDataRejectsScopeAndAuthDrift(t *testing.T) {
	for _, config := range []LocalDataConfig{{"example.com", "192.168.1.1"}, {"atlas-test-worker3.orb.local", "0.0.0.0/0"}, {"atlas-test-worker3.orb.local", "8.8.8.8"}, {"atlas-test-worker3.orb.local", "::1"}} {
		if config.Validate() == nil {
			t.Fatal("accepted unsafe config", config)
		}
	}
	for _, test := range []string{"pg-tls", "pg-certificate", "redis-plaintext", "redis-duplicate", "multi-broker", "listener", "missing-data"} {
		t.Run(test, func(t *testing.T) {
			base, model := dataFixture(t)
			switch test {
			case "pg-tls", "pg-certificate":
				xs := decode(t, base[PostgreSQLPath])
				cr := mustGet(t, xs, "Cluster", "service-postgresql", "postgresql")
				if test == "pg-tls" {
					obj(at(cr, "spec", "postgresql", "parameters"))["ssl_min_protocol_version"] = "TLSv1"
				} else {
					obj(cr["spec"])["certificates"] = Object{"serverTLSSecret": "custom"}
				}
				base[PostgreSQLPath] = list(xs)
			case "redis-plaintext", "redis-duplicate":
				xs := decode(t, base[RedisPath])
				cm := mustGet(t, xs, "ConfigMap", "service-redis", "redis-config")
				conf := at(cm, "data", "redis.conf").(string)
				if test == "redis-plaintext" {
					conf = strings.Replace(conf, "port 0", "port 6379", 1)
				} else {
					conf += "port 6379\n"
				}
				obj(cm["data"])["redis.conf"] = conf
				base[RedisPath] = list(xs)
			case "multi-broker", "listener":
				xs := decode(t, base[KafkaPath])
				if test == "multi-broker" {
					pool := mustGet(t, xs, "KafkaNodePool", "service-kafka", "single")
					obj(pool["spec"])["replicas"] = 2
				} else {
					cr := mustGet(t, xs, "Kafka", "service-kafka", "kafka")
					obj(arr(at(cr, "spec", "kafka", "listeners"))[0])["tls"] = false
				}
				base[KafkaPath] = list(xs)
			case "missing-data":
				delete(base, KafkaPath)
			}
			if _, err := RenderLocalData(base, model, LocalDataConfig{"atlas-example-worker3.orb.local", "192.168.138.1"}); err == nil {
				t.Fatal("accepted unsupported source")
			}
		})
	}
}

func TestFlinkOverviewOnly(t *testing.T) {
	base, model := dataFixture(t)
	r, err := RenderWithFlink(base, model, 48443)
	if err != nil {
		t.Fatal(err)
	}
	route := mustGet(t, decode(t, r.Routes[workload.EdgePath]), "HTTPRoute", "service-flink-session", "management-flink-https")
	matches := arr(at(obj(arr(at(route, "spec", "rules"))[0]), "matches"))
	accepts := func(method, path string) bool {
		for _, v := range matches {
			m := obj(v)
			if m["method"] != method {
				continue
			}
			p := obj(m["path"])
			value := p["value"].(string)
			switch p["type"] {
			case "Exact":
				if path == value {
					return true
				}
			case "PathPrefix":
				if strings.HasPrefix(path, value) {
					return true
				}
			case "RegularExpression":
				if regexp.MustCompile(value).MatchString(path) {
					return true
				}
			}
		}
		return false
	}
	for _, path := range []string{"/", "/main.abc123.js", "/assets/logo.svg", "/overview", "/config", "/jobs/overview", "/taskmanagers"} {
		if !accepts("GET", path) {
			t.Fatal("missing overview", path)
		}
	}
	for _, path := range []string{"/jobs/abc/yarn-cancel", "/jobs/abc/cancel", "/jobs/abc/cancel-with-savepoint/", "/jars/upload", "/jobs/abc/savepoints", "/jobs/abc/rescaling", "/v1/jobs/abc/yarn-cancel"} {
		for _, method := range []string{"GET", "HEAD", "POST", "PATCH", "DELETE"} {
			if accepts(method, path) {
				t.Fatal("unsafe Flink action routed", method, path)
			}
		}
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		if accepts(method, "/overview") {
			t.Fatal("write allowed")
		}
	}
}

func TestNativePolicyExceptionCannotEscapeIntoProjectsOrBroadNetworks(t *testing.T) {
	for _, test := range []string{"broad", "public", "extra-peer", "egress", "selector", "port", "namespace", "name"} {
		t.Run(test, func(t *testing.T) {
			base, model := dataFixture(t)
			result, err := RenderLocalData(base, model, LocalDataConfig{"atlas-example-worker3.orb.local", "192.168.138.1"})
			if err != nil {
				t.Fatal(err)
			}
			xs := decode(t, result.Services[PostgreSQLPath])
			np := mustGet(t, xs, "NetworkPolicy", "service-postgresql", "postgresql-local")
			ingress := obj(arr(at(np, "spec", "ingress"))[0])
			peers := arr(ingress["from"])
			switch test {
			case "broad":
				obj(at(obj(peers[0]), "ipBlock"))["cidr"] = "192.168.138.0/24"
			case "public":
				obj(at(obj(peers[0]), "ipBlock"))["cidr"] = "8.8.8.8/32"
			case "extra-peer":
				ingress["from"] = append(peers, Object{"podSelector": Object{}})
			case "egress":
				obj(np["spec"])["egress"] = []any{Object{}}
			case "selector":
				obj(at(np, "spec", "podSelector"))["matchLabels"] = Object{}
			case "port":
				obj(arr(ingress["ports"])[0])["port"] = 80
			case "namespace":
				obj(np["metadata"])["namespace"] = "project-example"
			case "name":
				obj(np["metadata"])["name"] = "another"
			}
			result.Services[PostgreSQLPath] = list(xs)
			if _, err := workload.InventoryOf(overlay(base, result.Certificates, result.Services), model); err == nil {
				t.Fatal("widened native exception", test)
			}
		})
	}
}
