package workload

import (
	"net/netip"
	"reflect"

	"github.com/snkio027/atlas/internal/platformnames"
)

// localDataIngress is a finite external-client exception. It cannot authorize
// project policies, pod CIDRs, broad networks, egress or alternate data services.
// The execution plan separately proves the /32 belongs to the host, not a Pod.
func localDataIngress(o Object, owner, file string) bool {
	var name, namespace string
	var port int
	var selector Object
	switch owner {
	case "postgresql":
		name, namespace, port = "postgresql-local", platformnames.PostgreSQL, 5432
		selector = Object{"cnpg.io/cluster": "postgresql", "cnpg.io/instanceRole": "primary"}
	case "redis":
		name, namespace, port = "redis-local", platformnames.Redis, 6379
		selector = Object{"app.kubernetes.io/name": "redis"}
	default:
		return false
	}
	if file != "gitops/platform/data/"+owner+"/overlays/development/resources.json" || o["apiVersion"] != "networking.k8s.io/v1" || o["kind"] != "NetworkPolicy" || meta(o)["name"] != name || meta(o)["namespace"] != namespace {
		return false
	}
	ingress := arr(val(o, "spec", "ingress"))
	if len(ingress) != 1 {
		return false
	}
	peers := arr(val(obj(ingress[0]), "from"))
	if len(peers) != 1 {
		return false
	}
	cidr := str(val(obj(peers[0]), "ipBlock", "cidr"))
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() != 32 || prefix.String() != cidr {
		return false
	}
	want := Object{"podSelector": Object{"matchLabels": selector}, "policyTypes": []any{"Ingress"}, "ingress": []any{Object{"from": []any{Object{"ipBlock": Object{"cidr": cidr}}}, "ports": []any{Object{"protocol": "TCP", "port": float64(port)}}}}}
	return reflect.DeepEqual(o["spec"], want)
}
