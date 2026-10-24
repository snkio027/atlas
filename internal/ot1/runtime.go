package ot1

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (x *Executor) AtlasGate(ctx context.Context, index int, snapshot Snapshot, baseline *Snapshot, dir string) (Snapshot, *GateProof, error) {
	phase := x.Plan.Phases[index]
	if e := x.fence(ctx, phase.Revision); e != nil {
		return snapshot, nil, e
	}
	config, lock, e := atlas.Load(x.RuntimeRepository, "profiles/ot1.json")
	if e != nil {
		return snapshot, nil, e
	}
	if config.Cluster != x.Plan.Target.Cluster || config.Revision != x.Plan.Branch {
		return snapshot, nil, errors.New("runtime profile does not match plan")
	}
	readonly := &ReadOnlyRunner{Delegate: x.Runner}
	app := &atlas.App{Root: x.RuntimeRepository, Config: config, Lock: lock, Runner: readonly}
	report := app.Status(ctx)
	if report.State != atlas.Adopted {
		return snapshot, nil, fmt.Errorf("existing engine does not report ADOPTED: %s: %s", report.State, report.Detail)
	}
	// Runtime gates retain the full existing node image/role/taint/readiness
	// checks independently of schema-3 durable authority status.
	if e = app.VerifyNodes(ctx); e != nil {
		return snapshot, nil, e
	}
	before, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return snapshot, nil, e
	}
	beforeIDs := IdentityDigest(snapshot)
	readonly.Requests = nil
	if e = app.Apply(ctx, x.Plan.Target.Cluster, true); e != nil {
		return snapshot, nil, e
	}
	after, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return snapshot, nil, e
	}
	runtime, e := x.runtime(ctx)
	if e != nil {
		return snapshot, nil, e
	}
	commands := []any{}
	for _, q := range readonly.Requests {
		args := []any{}
		for _, a := range q.Args {
			args = append(args, a)
		}
		commands = append(commands, observation.Object{"tool": q.Tool, "args": args, "inputSHA256": observation.SHA(q.Input), "exitCode": 0})
	}
	artifacts := map[string]observation.Object{
		"status.json":       {"state": string(report.State), "detail": report.Detail},
		"repeat-apply.json": {"exitCode": 0, "deniedWrites": readonly.Denied, "beforeIdentitySHA256": beforeIDs, "afterIdentitySHA256": "", "requests": commands},
		"audit-before.json": before, "audit-after.json": after, "runtime.json": runtime,
	}
	return x.closeAtlasGate(ctx, index, snapshot, baseline, dir, artifacts)
}

// Gate inputs (including repeat apply and runtime checks) are collected once.
// Only this final read-only closure can resample; it cannot replay those inputs.
func (x *Executor) closeAtlasGate(ctx context.Context, index int, snapshot Snapshot, baseline *Snapshot, dir string, artifacts map[string]observation.Object) (Snapshot, *GateProof, error) {
	phase := x.Plan.Phases[index]
	beforeIDs := IdentityDigest(snapshot)
	latest, e := x.captureCoherent(ctx, index, baseline, &snapshot)
	if e != nil {
		return snapshot, nil, e
	}
	checked := Assess(x.Plan, index, latest, baseline, &snapshot, x.Desired, nil)
	if checked.Ownership != "VERIFIED" || IdentityDigest(latest) != beforeIDs {
		return latest, nil, errors.New("repeat apply changed identity or post-gate observation failed")
	}
	if e = x.fence(ctx, phase.Revision); e != nil {
		return latest, nil, e
	}
	artifacts["repeat-apply.json"]["afterIdentitySHA256"] = IdentityDigest(latest)
	stem := stageStem(index, phase.Stage.Name)
	for _, name := range []string{"status.json", "repeat-apply.json", "audit-before.json", "audit-after.json", "runtime.json"} {
		artifact := GateArtifact{Schema: 1, Kind: name, PlanSHA256: observation.Digest(x.Plan), Target: x.Plan.Target, Implementation: x.Plan.Implementation, Revision: phase.Revision, SnapshotSHA256: observation.Digest(latest), Data: artifacts[name]}
		if e = observation.CreatePrivate(filepath.Join(dir, stem+"-"+name), observation.Bytes(artifact)); e != nil {
			return latest, nil, e
		}
	}
	gate, e := LoadGateEvidence(dir, stem, x.Plan, phase, latest)
	return latest, gate, e
}
func readAudit(repo string) (observation.Object, error) {
	files, e := filepath.Glob(filepath.Join(repo, ".state/audit/*"))
	if e != nil || len(files) == 0 {
		return nil, errors.New("audit files unavailable")
	}
	ids := []any{}
	events := []any{}
	seen := map[string]bool{}
	raw := bytes.Buffer{}
	fileHashes := map[string]string{}
	for _, file := range files {
		info, e := os.Lstat(file)
		if e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("audit file is not regular")
		}
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, e
		}
		fileHashes[filepath.Base(file)] = observation.SHA(b)
		raw.Write(b)
		scanner := bufio.NewScanner(bytes.NewReader(b))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var event observation.Object
			if e = observation.Decode(scanner.Bytes(), &event, false); e != nil {
				return nil, errors.New("incomplete audit JSON; preserve the attempt")
			}
			if event["stage"] == "ResponseComplete" {
				if event["level"] != "Metadata" || event["requestObject"] != nil || event["responseObject"] != nil {
					return nil, errors.New("audit must contain Metadata-only records")
				}
				events = append(events, event)
			}
			if strings.HasPrefix(observation.String(event["userAgent"]), "kubectl/") && event["stage"] == "ResponseComplete" {
				verb := observation.String(event["verb"])
				switch verb {
				case "create", "patch", "update", "delete", "deletecollection":
					id := observation.String(event["auditID"])
					if id == "" || seen[id] {
						return nil, errors.New("audit identity unavailable/duplicated")
					}
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		if e = scanner.Err(); e != nil {
			return nil, e
		}
	}
	return observation.Object{"complete": true, "rawSHA256": observation.SHA(raw.Bytes()), "fileSHA256": fileHashes, "kubectlMutationAuditIDs": ids, "events": events}, nil
}
func (x *Executor) runtime(ctx context.Context) (observation.Object, error) {
	result := observation.Object{}
	nodes, e := x.Reader.List(ctx, observation.Ref{APIVersion: "v1", Kind: "Node"})
	if e != nil {
		return nil, e
	}
	result["nodes"] = asAny(nodes)
	namespaces, e := x.Reader.List(ctx, observation.Ref{APIVersion: "v1", Kind: "Namespace"})
	if e != nil {
		return nil, e
	}
	allPods := []observation.Object{}
	for _, ns := range namespaces {
		pods, e := x.Reader.List(ctx, observation.Ref{APIVersion: "v1", Kind: "Pod", Namespace: observation.Reference(ns).Name})
		if e != nil {
			return nil, e
		}
		allPods = append(allPods, pods...)
	}
	result["pods"] = asAny(allPods)
	pvc, e := x.Reader.Read(ctx, observation.Ref{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "workload-web", Name: "web-data"})
	if e != nil || pvc == nil {
		return nil, errors.New("workload PVC unavailable")
	}
	result["pvc"] = pvc
	pv, e := x.Reader.Read(ctx, observation.Ref{APIVersion: "v1", Kind: "PersistentVolume", Name: observation.String(observation.At(pvc, "spec", "volumeName"))})
	if e != nil || pv == nil {
		return nil, errors.New("workload PV unavailable")
	}
	result["pv"] = pv
	// The ordinary observer never reads Secrets. This exact runtime TLS check
	// reads only the public CA certificate field, never the Secret JSON/key.
	prefix := []string{"--kubeconfig", filepath.Join(x.RuntimeRepository, ".state/kubeconfig"), "--context", x.Plan.Target.Context, "--request-timeout=30s"}
	b, e := x.Runner.Run(ctx, atlas.Request{Tool: "kubectl", Args: append(prefix, "get", "secret", "development-ca", "-n", "atlas-gateway", "-o", `jsonpath={.data.tls\.crt}`)})
	if e != nil {
		return nil, e
	}
	pem, e := base64.StdEncoding.DecodeString(string(b))
	if e != nil {
		return nil, errors.New("CA certificate encoding invalid")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("public development CA invalid")
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		if port != "18080" && port != "18443" {
			return nil, errors.New("unreviewed ingress port")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
	}}}
	defer client.CloseIdleConnections()
	for _, scheme := range []string{"http", "https"} {
		port := "18080"
		if scheme == "https" {
			port = "18443"
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://web.atlas.test:"+port+"/", nil)
		if e != nil {
			return nil, e
		}
		resp, e := client.Do(req)
		if e != nil {
			return nil, errors.New("runtime HTTP/TLS request failed")
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if e != nil {
			return nil, e
		}
		value := observation.Object{"status": resp.StatusCode, "location": resp.Header.Get("Location")}
		if scheme == "https" {
			value["tlsVerified"] = resp.TLS != nil && len(resp.TLS.VerifiedChains) > 0
			value["caSHA256"] = observation.SHA(pem)
			if strings.HasPrefix(string(body), "Atlas development web:") {
				value["bodyPrefix"] = "Atlas development web:"
			}
		}
		result[scheme] = value
	}
	return result, runtimeEvidence(result, x.Plan.Target.Cluster)
}
func asAny(objects []observation.Object) []any {
	out := []any{}
	for _, o := range objects {
		out = append(out, o)
	}
	return out
}

func allAuditIDs(audit observation.Object) map[string]bool {
	out := map[string]bool{}
	for _, v := range observation.Slice(audit["events"]) {
		id := observation.String(observation.Map(v)["auditID"])
		out[id] = true
	}
	return out
}
func (x *Executor) auditScope() error {
	if x.baselineAudit == nil {
		return errors.New("baseline audit fence unavailable")
	}
	audit, e := readAudit(x.RuntimeRepository)
	if e != nil {
		return e
	}
	current := allAuditIDs(audit)
	for id := range x.baselineAudit {
		if id == "" || !current[id] {
			return errors.New("audit history missing or rotated outside captured scope")
		}
	}
	kinds := map[string]string{"namespaces": "Namespace", "resourcequotas": "ResourceQuota", "limitranges": "LimitRange", "networkpolicies": "NetworkPolicy"}
	scope := map[string]bool{}
	for _, item := range x.Plan.Scope.Objects {
		scope[item.Identity] = true
	}
	for _, raw := range observation.Slice(audit["events"]) {
		event := observation.Map(raw)
		id := observation.String(event["auditID"])
		if x.baselineAudit[id] {
			continue
		}
		ref := observation.Map(event["objectRef"])
		kind := kinds[observation.String(ref["resource"])]
		if kind == "" {
			continue
		}
		version := observation.String(ref["apiVersion"])
		if version == "" {
			version = "v1"
		}
		if group := observation.String(ref["apiGroup"]); group != "" {
			version = group + "/" + version
		}
		namespace := observation.String(ref["namespace"])
		if kind == "Namespace" {
			// Kubernetes audit request metadata may repeat the Namespace name in
			// objectRef.namespace. The transferred object itself is cluster-scoped.
			namespace = ""
		}
		identity := observation.Ref{APIVersion: version, Kind: kind, Namespace: namespace, Name: observation.String(ref["name"])}
		if !scope[identity.Key()] {
			continue
		}
		if kind == "Namespace" && observation.String(ref["namespace"]) != "" && observation.String(ref["namespace"]) != identity.Name {
			return errors.New("inconsistent Namespace audit identity")
		}
		user := observation.String(observation.At(event, "user", "username"))
		subresource := observation.String(ref["subresource"])
		if user == "system:serviceaccount:argocd:argocd-application-controller" {
			continue
		}
		if subresource == "status" && (user == "system:kube-controller-manager" || kind == "ResourceQuota" && user == "system:serviceaccount:kube-system:resourcequota-controller" || kind == "Namespace" && user == "system:serviceaccount:kube-system:namespace-controller") {
			continue
		}
		return errors.New("non-Argo write to a transferred resource in API audit")
	}
	return nil
}
