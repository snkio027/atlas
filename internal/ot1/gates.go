package ot1

import (
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"path/filepath"
	"strings"
)

func stageStem(i int, name string) string { return fmt.Sprintf("%02d-%s", i, name) }

// GateArtifact is the wire contract for independently collected engine/runtime
// evidence. This verifier does not execute apply or turn an assertion into proof.
// The operator must retain the actual command runner and Metadata-only audit
// payloads, bound to the same plan, binary, target and snapshot.
type GateArtifact struct {
	Schema         int                        `json:"schema"`
	Kind           string                     `json:"kind"`
	PlanSHA256     string                     `json:"planSHA256"`
	Target         observation.Target         `json:"target"`
	Implementation observation.Implementation `json:"implementation"`
	Revision       string                     `json:"revision"`
	SnapshotSHA256 string                     `json:"snapshotSHA256"`
	Data           observation.Object         `json:"data"`
}

func LoadGateEvidence(dir, stem string, plan Plan, phase Phase, snapshot Snapshot) (*GateProof, error) {
	names := []string{"status.json", "repeat-apply.json", "audit-before.json", "audit-after.json", "runtime.json"}
	files := map[string]GateArtifact{}
	hashes := map[string]string{}
	for _, name := range names {
		b, e := observation.RegularPrivate(filepath.Join(dir, stem+"-"+name))
		if e != nil {
			return nil, e
		}
		var artifact GateArtifact
		if e = observation.Decode(b, &artifact, true); e != nil {
			return nil, e
		}
		if artifact.Schema != 1 || artifact.Kind != name || artifact.PlanSHA256 != observation.Digest(plan) || artifact.Target != plan.Target || artifact.Implementation != plan.Implementation || artifact.Revision != phase.Revision || artifact.SnapshotSHA256 != observation.Digest(snapshot) {
			return nil, errors.New("gate artifact source binding mismatch")
		}
		files[name] = artifact
		hashes[name] = observation.SHA(b)
	}
	if observation.String(files["status.json"].Data["state"]) != string(atlas.Adopted) {
		return nil, errors.New("normal Bootstrap is not ADOPTED")
	}
	repeated := files["repeat-apply.json"].Data
	if observation.Digest(repeated["exitCode"]) != observation.Digest(0) || observation.Digest(repeated["deniedWrites"]) != observation.Digest(0) {
		return nil, errors.New("repeat apply failed or attempted mutation")
	}
	ids := IdentityDigest(snapshot)
	if observation.String(repeated["beforeIdentitySHA256"]) != ids || observation.String(repeated["afterIdentitySHA256"]) != ids {
		return nil, errors.New("repeat apply identity fence mismatch")
	}
	requests := observation.Slice(repeated["requests"])
	if len(requests) == 0 {
		return nil, errors.New("runner evidence missing")
	}
	for _, value := range requests {
		r := observation.Map(value)
		args := []string{}
		for _, arg := range observation.Slice(r["args"]) {
			text, ok := arg.(string)
			if !ok {
				return nil, errors.New("invalid runner argument")
			}
			args = append(args, text)
		}
		if !ReadOnlyRequest(atlas.Request{Tool: observation.String(r["tool"]), Args: args}) || observation.Digest(r["exitCode"]) != observation.Digest(0) {
			return nil, errors.New("runner trace contains denied/failed request")
		}
	}
	before, e := auditIDs(files["audit-before.json"].Data)
	if e != nil {
		return nil, e
	}
	after, e := auditIDs(files["audit-after.json"].Data)
	if e != nil {
		return nil, e
	}
	if observation.Digest(before) != observation.Digest(after) {
		return nil, errors.New("repeat apply audit mutation set changed")
	}
	runtime := files["runtime.json"].Data
	if e = runtimeEvidence(runtime, plan.Target.Cluster); e != nil {
		return nil, e
	}
	g := &GateProof{validated: true, Stage: phase.Stage.Name, PlanSHA256: observation.Digest(plan), SnapshotSHA256: observation.Digest(snapshot), EngineStatus: "ADOPTED", RepeatApplyExit: 0, DeniedWrites: 0, AuditBefore: len(before), AuditAfter: len(after), RuntimeVerified: true, EvidenceFiles: hashes}
	return g, g.Validate(plan, phase, snapshot)
}
func IdentityDigest(s Snapshot) string {
	values := map[string]string{}
	raw := rawIndex(s.Envelope.Raw)
	for _, ref := range append(identityRefs(), AppRef("atlas-refactor-root"), AppRef("argocd-self")) {
		o := raw[ref.Key()]
		values[ref.Key()] = observation.String(observation.At(o, "metadata", "uid")) + ":" + observation.Digest(observation.Semantic(o))
	}
	// Application status/runtime metadata are excluded; full specs remain bound.
	return observation.Digest(values)
}
func auditIDs(data observation.Object) (map[string]bool, error) {
	// A complete pre/post set, not a count that could hide log truncation/rotation.
	if data["complete"] != true || !observation.Hash(observation.String(data["rawSHA256"])) {
		return nil, errors.New("audit capture incomplete")
	}
	events, ok := data["kubectlMutationAuditIDs"].([]any)
	if !ok {
		return nil, errors.New("audit IDs missing")
	}
	out := map[string]bool{}
	for _, raw := range events {
		id := observation.String(raw)
		if id == "" || out[id] {
			return nil, errors.New("duplicate/missing audit ID")
		}
		out[id] = true
	}
	return out, nil
}
func runtimeEvidence(data observation.Object, cluster string) error {
	objects := func(key string) []observation.Object {
		out := []observation.Object{}
		for _, raw := range observation.Slice(data[key]) {
			out = append(out, observation.Map(raw))
		}
		return out
	}
	if e := checkNodes(objects("nodes"), nil, cluster); e != nil {
		return e
	}
	namespaces := map[string]bool{"kube-system": false, "argocd": false, "workload-web": false, "atlas-secrets": false, "atlas-monitoring": false, "atlas-storage": false, "envoy-gateway-system": false}
	web, gateway := false, false
	for _, pod := range objects("pods") {
		if observation.String(observation.At(pod, "status", "phase")) == "Succeeded" {
			continue
		}
		ready := false
		for _, c := range observation.Slice(observation.At(pod, "status", "conditions")) {
			m := observation.Map(c)
			if m["type"] == "Ready" && m["status"] == "True" {
				ready = true
			}
		}
		ref := observation.Reference(pod)
		if ref.Kind != "Pod" || observation.String(observation.At(pod, "metadata", "uid")) == "" || observation.At(pod, "metadata", "deletionTimestamp") != nil || observation.String(observation.At(pod, "status", "phase")) != "Running" || !ready {
			return errors.New("runtime Pod is not Ready")
		}
		if _, ok := namespaces[ref.Namespace]; ok {
			namespaces[ref.Namespace] = true
		}
		if ref.Namespace == "workload-web" {
			if observation.String(observation.At(pod, "spec", "nodeName")) != cluster+"-worker3" {
				return errors.New("runtime workload Pod outside data node")
			}
			web = true
		}
		if ref.Namespace == "envoy-gateway-system" && strings.HasPrefix(ref.Name, "envoy-atlas-gateway-") &&
			observation.String(observation.At(pod, "metadata", "labels", "app.kubernetes.io/component")) == "proxy" &&
			observation.String(observation.At(pod, "metadata", "labels", "gateway.envoyproxy.io/owning-gateway-name")) == "development" &&
			observation.String(observation.At(pod, "metadata", "labels", "gateway.envoyproxy.io/owning-gateway-namespace")) == "atlas-gateway" {
			if observation.String(observation.At(pod, "spec", "nodeName")) != cluster+"-worker" {
				return errors.New("runtime gateway Pod outside gateway node")
			}
			gateway = true
		}
	}
	for namespace, present := range namespaces {
		if !present {
			return fmt.Errorf("platform Pod inventory incomplete: %s", namespace)
		}
	}
	if !web || !gateway {
		return errors.New("gateway/data placement not proven")
	}
	pvc := observation.Map(data["pvc"])
	if observation.Reference(pvc) != (observation.Ref{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "workload-web", Name: "web-data"}) || observation.String(observation.At(pvc, "status", "phase")) != "Bound" || observation.String(observation.At(pvc, "spec", "volumeName")) == "" {
		return errors.New("workload PVC not Bound")
	}
	pv := observation.Map(data["pv"])
	if observation.Reference(pv) != (observation.Ref{APIVersion: "v1", Kind: "PersistentVolume", Name: observation.String(observation.At(pvc, "spec", "volumeName"))}) || observation.String(observation.At(pv, "spec", "persistentVolumeReclaimPolicy")) != "Retain" {
		return errors.New("PV identity/reclaim drift")
	}
	if observation.Digest(observation.At(data, "http", "status")) != observation.Digest(301) || observation.String(observation.At(data, "http", "location")) != "https://web.atlas.test:18443/" || observation.Digest(observation.At(data, "https", "status")) != observation.Digest(200) || observation.At(data, "https", "tlsVerified") != true || !observation.Hash(observation.String(observation.At(data, "https", "caSHA256"))) || observation.String(observation.At(data, "https", "bodyPrefix")) != "Atlas development web:" {
		return errors.New("HTTP/TLS runtime evidence failed")
	}
	return nil
}
