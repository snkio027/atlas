// Package platformnames owns the namespace layout of new installations.
// Historical schemas and ceremony fixtures retain their own bound layout.
package platformnames

import (
	"atlas-refactor/internal/authored"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed layout.yaml
var definition []byte

type Namespace struct {
	Unit             string `json:"unit"`
	Name             string `json:"name"`
	Class            string `json:"class"`
	Domain           string `json:"domain"`
	DeclarativeOwner string `json:"declarativeOwner"`
	Lifecycle        string `json:"lifecycle"`
	Owner            string `json:"owner"`
	PodSecurity      string `json:"podSecurity"`
	Deletion         string `json:"deletion"`
}
type Layout struct {
	Version    string      `json:"version"`
	Namespaces []Namespace `json:"namespaces"`
}

const Version = "role-scoped-v1"

var layout = load()

func load() Layout {
	b, e := authored.JSON(definition)
	if e != nil {
		panic(e)
	}
	var l Layout
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&l); e != nil {
		panic(e)
	}
	seen := map[string]bool{}
	if l.Version != Version {
		panic("unknown namespace layout")
	}
	for _, n := range l.Namespaces {
		classOK := n.Class == "platform" || n.Class == "service" || n.Class == "project"
		psaOK := n.PodSecurity == "restricted" || n.PodSecurity == "privileged" || n.PodSecurity == "unchanged"
		if !classOK || !psaOK || n.Unit == "" || n.Domain == "" || seen[n.Name] || seen["unit:"+n.Unit] || !strings.HasPrefix(n.Name, n.Class+"-") || n.Owner != "platform" || n.DeclarativeOwner == "" || n.Deletion != "explicit-retirement-only" {
			panic("invalid namespace layout")
		}
		seen[n.Name] = true
		seen["unit:"+n.Unit] = true
	}
	return l
}
func Definitions() []Namespace { return append([]Namespace(nil), layout.Namespaces...) }
func Digest() string           { s := sha256.Sum256(definition); return hex.EncodeToString(s[:]) }
func Get(unit string) Namespace {
	for _, n := range layout.Namespaces {
		if n.Unit == unit {
			return n
		}
	}
	panic("unknown built-in namespace unit: " + unit)
}
func Name(unit string) string { return Get(unit).Name }

var (
	Argo            = Name("argocd")
	CertManager     = Name("cert-manager")
	Gateway         = Name("gateway")
	Monitoring      = Name("monitoring")
	Logging         = Name("logging")
	Secrets         = Name("sealed-secrets")
	ObjectStorage   = Name("object-storage")
	Smoke           = Name("workload-web")
	CNPGOperator    = Name("cnpg-operator")
	StrimziOperator = Name("strimzi-operator")
	FlinkOperator   = Name("flink-operator")
	PostgreSQL      = Name("postgresql")
	Redis           = Name("redis")
	Kafka           = Name("kafka")
	Flink           = Name("flink")
	S3Address       = "seaweedfs." + ObjectStorage + ".svc.cluster.local:8333"
)

func Project(id string) string { return "project-" + id }
func Reserved(name string) bool {
	if strings.HasPrefix(name, "platform-") || strings.HasPrefix(name, "service-") || strings.HasPrefix(name, "project-") || name == "platform-smoke" {
		return true
	}
	for _, n := range layout.Namespaces {
		if name == n.Name || name == n.Unit {
			return true
		}
	}
	switch name {
	case "default", "kube-system", "kube-public", "kube-node-lease", "local-path-storage", "envoy-gateway-system":
		return true
	}
	return Legacy(name)
}
func Legacy(name string) bool {
	switch name {
	case "atlas-gateway", "atlas-monitoring", "atlas-secrets", "atlas-storage", "argocd", "cert-manager", "envoy-gateway-system", "gateway", "monitoring", "logging", "sealed-secrets", "object-storage", "workload-web", "postgresql", "redis", "kafka", "flink":
		return true
	}
	return false
}
func ValidateDefinitions(names []string) error {
	seen := map[string]bool{}
	known := map[string]bool{"default": true, "kube-system": true, "kube-public": true, "kube-node-lease": true, "local-path-storage": true}
	for _, n := range layout.Namespaces {
		known[n.Name] = true
	}
	for _, name := range names {
		if seen[name] || !known[name] {
			return fmt.Errorf("unsupported/duplicate namespace %q; use its instance-bound product", name)
		}
		seen[name] = true
	}
	for _, name := range []string{Argo, CertManager, Gateway, Monitoring, Secrets, ObjectStorage, Smoke} {
		if !seen[name] {
			return fmt.Errorf("installation lacks current namespace %q", name)
		}
	}
	for _, pair := range [][2]string{{PostgreSQL, CNPGOperator}, {Kafka, StrimziOperator}, {Flink, FlinkOperator}} {
		if seen[pair[0]] != seen[pair[1]] {
			return fmt.Errorf("incomplete operator/service layout: %v", pair)
		}
	}
	return nil
}
