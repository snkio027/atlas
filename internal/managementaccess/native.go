package managementaccess

import (
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/workload"
	"net/netip"
	"regexp"
	"strings"

	"github.com/snkio027/atlas/internal/platform"
	"github.com/snkio027/atlas/internal/platformnames"
)

const (
	PostgreSQLPath     = "gitops/platform/data/postgresql/overlays/development/resources.json"
	RedisPath          = "gitops/platform/data/redis/overlays/development/resources.json"
	KafkaPath          = "gitops/platform/data/kafka/overlays/development/resources.json"
	DataPKIPath        = "gitops/platform/data/data-pki/overlays/development/resources.json"
	PostgreSQLPort     = 30432
	RedisPort          = 30679
	KafkaBootstrapPort = 30993
	KafkaBrokerPort    = 30994
)

// LocalDataConfig is the reviewed deployment identity, not a generic exposure API.
// ClientAddress must be measured with a credential-free, separately approved
// Local NodePort preflight on this target. It must not be inferred from DNS.
type LocalDataConfig struct {
	NodeHost      string `json:"nodeHost"`
	ClientAddress string `json:"clientAddress"`
}

func (c LocalDataConfig) Validate() error {
	if !regexp.MustCompile(`^atlas-[a-z0-9]+(?:-[a-z0-9]+)*-worker[0-9]*\.orb\.local$`).MatchString(c.NodeHost) {
		return errors.New("expected an Atlas OrbStack worker hostname")
	}
	ip, err := netip.ParseAddr(c.ClientAddress)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return errors.New("expected one observed private IPv4 client address, never a CIDR or wildcard")
	}
	return nil
}

type LocalDataResult struct {
	Certificates Files // Publish and prove current certificates before opening ports.
	Services     Files
}

// RenderLocalData produces two local GitOps deltas for the existing single-data-
// node topology. It does not read credentials, reach a cluster or publish Git.
func RenderLocalData(base Files, model *platform.ResourceModel, c LocalDataConfig) (LocalDataResult, error) {
	fail := func(err error) (LocalDataResult, error) { return LocalDataResult{}, err }
	if err := c.Validate(); err != nil {
		return fail(err)
	}
	if model == nil {
		return fail(errors.New("resource model required"))
	}
	inventory, err := workload.InventoryOf(base, model)
	if err != nil {
		return fail(err)
	}
	expected := map[string]string{PostgreSQLPath: "postgresql", RedisPath: "redis", KafkaPath: "kafka", DataPKIPath: "data-pki"}
	for _, r := range inventory {
		if owner, ok := expected[r.Path]; ok && owner != r.Owner {
			return fail(errors.New("data access source owner differs"))
		}
	}
	pg, err := platform.DecodeJSONManifests(base[PostgreSQLPath])
	if err != nil {
		return fail(err)
	}
	redis, err := platform.DecodeJSONManifests(base[RedisPath])
	if err != nil {
		return fail(err)
	}
	kafka, err := platform.DecodeJSONManifests(base[KafkaPath])
	if err != nil {
		return fail(err)
	}
	pki, err := platform.DecodeJSONManifests(base[DataPKIPath])
	if err != nil {
		return fail(err)
	}
	cluster, err := get(pg, "Cluster", platformnames.PostgreSQL, "postgresql")
	if err != nil {
		return fail(err)
	}
	if at(cluster, "spec", "instances") != float64(1) || at(cluster, "spec", "affinity", "nodeSelector", "node-role.local/data") != "true" || at(cluster, "spec", "enableSuperuserAccess") != false || at(cluster, "spec", "certificates") != nil {
		return fail(errors.New("unsupported PostgreSQL topology/certificate ownership"))
	}
	parameters := obj(at(cluster, "spec", "postgresql", "parameters"))
	if parameters["ssl_min_protocol_version"] != "TLSv1.3" {
		return fail(errors.New("PostgreSQL TLS contract differs"))
	}
	// CNPG owns issuance and its rw Service; this supplementary Service is owned
	// by the existing postgresql Application, and follows the same primary selector.
	obj(cluster["spec"])["certificates"] = Object{"serverAltDNSNames": []string{c.NodeHost}}
	sts, err := get(redis, "StatefulSet", platformnames.Redis, "redis")
	if err != nil {
		return fail(err)
	}
	cm, err := get(redis, "ConfigMap", platformnames.Redis, "redis-config")
	if err != nil {
		return fail(err)
	}
	settings := map[string]string{}
	raw, _ := at(cm, "data", "redis.conf").(string)
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 2 || settings[f[0]] != "" || f[0] == "include" {
			return fail(errors.New("unsupported Redis configuration"))
		}
		settings[f[0]] = strings.Join(f[1:], " ")
	}
	for key, want := range map[string]string{"port": "0", "tls-port": "6379", "tls-cert-file": "/tls/tls.crt", "tls-key-file": "/tls/tls.key", "tls-ca-cert-file": "/tls/ca.crt", "aclfile": "/auth/users.acl"} {
		if settings[key] != want {
			return fail(errors.New("Redis TLS/ACL baseline differs"))
		}
	}
	if at(sts, "spec", "replicas") != float64(1) || at(sts, "spec", "template", "spec", "nodeSelector", "node-role.local/data") != "true" {
		return fail(errors.New("Redis must use the single data worker"))
	}
	// Redis does not reload a replaced TLS file automatically. The service phase
	// rolls this one StatefulSet after cert readiness, with the existing PVC/auth.
	metadata := obj(at(sts, "spec", "template", "metadata"))
	annotations := obj(metadata["annotations"])
	if annotations == nil {
		annotations = Object{}
		metadata["annotations"] = annotations
	}
	if annotations["atlas.local/local-access-host"] != nil {
		return fail(errors.New("local access already configured"))
	}
	annotations["atlas.local/local-access-host"] = c.NodeHost
	cert, err := get(pki, "Certificate", platformnames.Redis, "server-tls")
	if err != nil {
		return fail(err)
	}
	if at(cert, "spec", "issuerRef", "name") != "service-ca" || at(cert, "spec", "secretName") != "server-tls" {
		return fail(errors.New("Redis certificate identity differs"))
	}
	obj(cert["spec"])["dnsNames"] = append(arr(at(cert, "spec", "dnsNames")), c.NodeHost)
	r := LocalDataResult{Certificates: Files{PostgreSQLPath: list(pg), DataPKIPath: list(pki)}, Services: Files{}}
	for _, target := range []struct {
		objects        []Object
		path, ns, name string
		port, nodePort int
		selector       Object
	}{
		{pg, PostgreSQLPath, platformnames.PostgreSQL, "postgresql-local", 5432, PostgreSQLPort, Object{"cnpg.io/cluster": "postgresql", "cnpg.io/instanceRole": "primary"}},
		{redis, RedisPath, platformnames.Redis, "redis-local", 6379, RedisPort, Object{"app.kubernetes.io/name": "redis"}},
	} {
		svc := resource("v1", "Service", target.ns, target.name, Object{"type": "NodePort", "externalTrafficPolicy": "Local", "internalTrafficPolicy": "Cluster", "sessionAffinity": "None", "selector": target.selector, "ports": []any{Object{"name": "tls", "protocol": "TCP", "port": target.port, "targetPort": target.port, "nodePort": target.nodePort}}})
		policy := resource("networking.k8s.io/v1", "NetworkPolicy", target.ns, target.name, Object{"podSelector": Object{"matchLabels": target.selector}, "policyTypes": []string{"Ingress"}, "ingress": []any{Object{"from": []any{Object{"ipBlock": Object{"cidr": c.ClientAddress + "/32"}}}, "ports": []any{Object{"protocol": "TCP", "port": target.port}}}}})
		r.Services[target.path] = list(append(target.objects, svc, policy))
	}
	cr, err := get(kafka, "Kafka", platformnames.Kafka, "kafka")
	if err != nil {
		return fail(err)
	}
	pool, err := get(kafka, "KafkaNodePool", platformnames.Kafka, "single")
	if err != nil {
		return fail(err)
	}
	if at(pool, "spec", "replicas") != float64(1) || at(pool, "metadata", "labels", "strimzi.io/cluster") != "kafka" || at(cr, "spec", "kafka", "rack") != nil {
		return fail(errors.New("local Kafka access requires reviewed single broker 0; live nodeIds must be checked before publication"))
	}
	listeners := arr(at(cr, "spec", "kafka", "listeners"))
	if len(listeners) != 1 || at(obj(listeners[0]), "name") != "tls" || at(obj(listeners[0]), "type") != "internal" || at(obj(listeners[0]), "tls") != true || at(obj(listeners[0]), "authentication", "type") != "tls" || at(cr, "spec", "kafka", "authorization", "type") != "simple" {
		return fail(errors.New("Kafka TLS/ACL baseline differs or listener already exists"))
	}
	// Strimzi's nodeport type requires a cluster-scoped init binding even with
	// explicit advertised addresses. Its internal listener supports the same
	// advertisedHost/Port and certificate SAN overrides without node discovery.
	// Keep that runtime listener Operator-owned and own only supplementary,
	// distinctly named Services in Git, as we already do for CNPG and Redis.
	obj(at(cr, "spec", "kafka"))["listeners"] = append(listeners, Object{"name": "local", "port": 9094, "type": "internal", "tls": true, "authentication": Object{"type": "tls"}, "networkPolicyPeers": []any{Object{"ipBlock": Object{"cidr": c.ClientAddress + "/32"}}}, "configuration": Object{"bootstrap": Object{"alternativeNames": []string{c.NodeHost}}, "brokers": []any{Object{"broker": 0, "advertisedHost": c.NodeHost, "advertisedPort": KafkaBrokerPort}}}})
	for _, target := range []struct {
		name string
		port int
	}{{"kafka-local-bootstrap", KafkaBootstrapPort}, {"kafka-local-0", KafkaBrokerPort}} {
		selector := Object{"strimzi.io/cluster": "kafka", "strimzi.io/kind": "Kafka", "strimzi.io/pool-name": "single", "strimzi.io/broker-role": "true", "strimzi.io/pod-name": "kafka-single-0"}
		kafka = append(kafka, resource("v1", "Service", platformnames.Kafka, target.name, Object{"type": "NodePort", "externalTrafficPolicy": "Local", "internalTrafficPolicy": "Cluster", "sessionAffinity": "None", "selector": selector, "ports": []any{Object{"name": "tls", "protocol": "TCP", "port": 9094, "targetPort": 9094, "nodePort": target.port}}}))
	}
	r.Services[KafkaPath] = list(kafka)
	if err := validatePhases(base, model, r.Certificates, r.Services); err != nil {
		return fail(fmt.Errorf("local data access: %w", err))
	}
	return r, nil
}
