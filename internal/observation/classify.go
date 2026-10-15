package observation

import (
	"encoding/json"
	"strconv"
)

func integer(v any) *int64 {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = n.String()
	case int:
		s = strconv.Itoa(n)
	case int64:
		s = strconv.FormatInt(n, 10)
	case float64:
		if n != float64(int64(n)) {
			return nil
		}
		s = strconv.FormatInt(int64(n), 10)
	default:
		return nil
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < 0 {
		return nil
	}
	return &n
}
func rank(c Classification) int {
	switch c {
	case Unknown:
		return 5
	case Drifted:
		return 4
	case Degraded:
		return 3
	case Absent:
		return 2
	case Progressing:
		return 1
	case Verified:
		return 0
	default:
		return 5
	}
}
func worse(a, b Classification) Classification {
	if rank(b) > rank(a) {
		return b
	}
	return a
}
func (f *ResourceFact) fail(c Classification, reason string) {
	f.Classification = worse(f.Classification, c)
	f.Reasons = append(f.Reasons, reason)
}
func conditions(o Object) []Condition {
	var out []Condition
	for _, v := range Slice(At(o, "status", "conditions")) {
		m := Map(v)
		out = append(out, Condition{String(m["type"]), String(m["status"]), String(m["reason"])})
	}
	return out
}
func hasCondition(f ResourceFact, name, status string) bool {
	for _, c := range f.Conditions {
		if c.Type == name && c.Status == status {
			return true
		}
	}
	return false
}
func resourceBase(ref Ref, o Object, readErr error) ResourceFact {
	f := ResourceFact{Ref: ref, Classification: Verified}
	if readErr != nil {
		f.fail(Unknown, "READ_UNAVAILABLE")
		return f
	}
	if o == nil {
		f.fail(Absent, "NOT_FOUND")
		return f
	}
	if Reference(o) != ref {
		f.fail(Unknown, "IDENTITY_MISMATCH")
		return f
	}
	m := Map(o["metadata"])
	f.UID = String(m["uid"])
	f.ResourceVersion = String(m["resourceVersion"])
	if f.UID == "" || f.ResourceVersion == "" {
		f.fail(Unknown, "MISSING_IDENTITY_EVIDENCE")
	}
	if m["deletionTimestamp"] != nil {
		f.fail(Degraded, "TERMINATING")
	}
	f.Generation = integer(m["generation"])
	f.ObservedGeneration = integer(At(o, "status", "observedGeneration"))
	if m["generation"] != nil && f.Generation == nil || At(o, "status", "observedGeneration") != nil && f.ObservedGeneration == nil {
		f.fail(Unknown, "MALFORMED_GENERATION")
	}
	if f.Generation != nil && f.ObservedGeneration != nil {
		if *f.ObservedGeneration < *f.Generation {
			f.fail(Progressing, "STALE_OBSERVED_GENERATION")
		}
		if *f.ObservedGeneration > *f.Generation {
			f.fail(Unknown, "FUTURE_OBSERVED_GENERATION")
		}
	}
	f.SemanticSHA256 = Digest(Semantic(o))
	f.Tracking = String(At(m, "annotations", TrackingAnnotation))
	f.Conditions = conditions(o)
	for _, raw := range Slice(m["managedFields"]) {
		field := Map(raw)
		if String(field["manager"]) == "argocd-controller" && String(field["operation"]) == "Apply" && String(field["subresource"]) == "" && len(Map(field["fieldsV1"])) > 0 {
			f.ArgoSSA = true
		}
	}
	return f
}
func ClassifyResource(expected ExpectedResource, o Object, readErr error) ResourceFact {
	f := resourceBase(expected.Ref, o, readErr)
	if readErr != nil || o == nil || Reference(o) != expected.Ref {
		return f
	}
	if expected.UID != "" && f.UID != expected.UID {
		f.fail(Drifted, "UID_CHANGED")
	}
	if expected.SemanticSHA256 != "" && f.SemanticSHA256 != expected.SemanticSHA256 {
		f.fail(Drifted, "SEMANTIC_DRIFT")
	}
	if expected.Tracking != "" && f.Tracking != expected.Tracking {
		f.fail(Drifted, "OWNERSHIP_MISMATCH")
	}
	if expected.RequireSSA && !f.ArgoSSA {
		f.fail(Unknown, "ARGO_SSA_NOT_PROVEN")
	}
	switch expected.Readiness {
	case "identity-content": // No implicit resource-specific health claim.
	case "namespace-active":
		if String(At(o, "status", "phase")) == "" {
			f.fail(Unknown, "NAMESPACE_PHASE_MISSING")
		} else if String(At(o, "status", "phase")) != "Active" {
			f.fail(Progressing, "NAMESPACE_NOT_ACTIVE")
		}
	case "pvc-bound":
		if String(At(o, "status", "phase")) == "" {
			f.fail(Unknown, "PVC_PHASE_MISSING")
		} else if String(At(o, "status", "phase")) != "Bound" || String(At(o, "spec", "volumeName")) == "" {
			f.fail(Progressing, "PVC_NOT_BOUND")
		}
	case "crd-established":
		if !hasCondition(f, "Established", "True") || !hasCondition(f, "NamesAccepted", "True") {
			f.fail(Progressing, "CRD_NOT_ESTABLISHED")
		}
	case "deployment-available":
		if f.Generation == nil || f.ObservedGeneration == nil {
			f.fail(Unknown, "GENERATION_EVIDENCE_MISSING")
		}
		desired := integer(At(o, "spec", "replicas"))
		if desired == nil {
			n := int64(1)
			desired = &n
		}
		for _, field := range []string{"updatedReplicas", "availableReplicas", "replicas"} {
			n := integer(At(o, "status", field))
			if n == nil {
				zero := int64(0)
				n = &zero
			}
			if *n != *desired {
				f.fail(Progressing, "DEPLOYMENT_REPLICAS_NOT_CONVERGED")
				break
			}
		}
		if !hasCondition(f, "Available", "True") {
			f.fail(Progressing, "DEPLOYMENT_NOT_AVAILABLE")
		}
	default:
		f.fail(Unknown, "UNSUPPORTED_READINESS_RULE")
	}
	return f
}
func ClassifyApplication(expected ExpectedApplication, o Object, readErr error) ApplicationFact {
	ref := Ref{"argoproj.io/v1alpha1", "Application", expected.ApplicationNamespace(), expected.Name}
	f := ApplicationFact{ResourceFact: resourceBase(ref, o, readErr), ExpectedRevision: expected.Revision, ExpectedSpecSHA256: Digest(expected.Spec)}
	if readErr != nil || o == nil || Reference(o) != ref {
		return f
	}
	if expected.UID != "" && expected.UID != f.UID {
		f.fail(Drifted, "UID_CHANGED")
	}
	f.ObservedSpecSHA256 = Digest(Map(o["spec"]))
	if f.ExpectedSpecSHA256 != f.ObservedSpecSHA256 {
		f.fail(Drifted, "APPLICATION_SPEC_DRIFT")
	}
	if len(Slice(At(o, "metadata", "finalizers"))) > 0 || len(Slice(At(o, "metadata", "ownerReferences"))) > 0 {
		f.fail(Drifted, "APPLICATION_DELETION_BOUNDARY")
	}
	s := Map(o["status"])
	f.ObservedRevision = String(At(s, "sync", "revision"))
	f.Sync = String(At(s, "sync", "status"))
	f.Health = String(At(s, "health", "status"))
	op := Map(s["operationState"])
	f.Operation = Operation{o["operation"] != nil, String(op["phase"]), String(At(op, "syncResult", "revision")), String(op["finishedAt"]), Slice(At(op, "operation", "info"))}
	if !FullSHA(f.ObservedRevision) {
		f.fail(Unknown, "REVISION_EVIDENCE_MISSING")
	} else if f.ObservedRevision != expected.Revision {
		f.fail(Progressing, "REVISION_NOT_CONVERGED")
	}
	switch f.Sync {
	case "Synced":
	case "OutOfSync":
		f.fail(Progressing, "OUT_OF_SYNC")
	default:
		f.fail(Unknown, "SYNC_UNKNOWN")
	}
	switch f.Health {
	case "Healthy":
	case "Progressing", "Suspended", "Missing":
		f.fail(Progressing, "HEALTH_NOT_READY")
	case "Degraded":
		f.fail(Degraded, "HEALTH_DEGRADED")
	default:
		f.fail(Unknown, "HEALTH_UNKNOWN")
	}
	switch f.Operation.Phase {
	case "Running", "Terminating":
		f.Operation.Active = true
	case "Failed", "Error":
		f.fail(Degraded, "LAST_OPERATION_FAILED")
	case "Succeeded", "":
	default:
		f.fail(Unknown, "OPERATION_UNKNOWN")
	}
	if f.Operation.Active {
		f.fail(Progressing, "OPERATION_ACTIVE")
	}
	if f.Operation.Phase != "" && !f.Operation.Active && f.Operation.FinishedAt == "" {
		f.fail(Unknown, "OPERATION_NOT_TERMINAL")
	}
	for _, c := range f.Conditions {
		switch c.Type {
		case "SharedResourceWarning":
			f.fail(Drifted, "SHARED_RESOURCE")
		case "ComparisonError", "InvalidSpecError", "SyncError":
			f.fail(Degraded, "APPLICATION_ERROR")
		default:
			f.fail(Unknown, "APPLICATION_CONDITION")
		}
	}
	for _, raw := range Slice(s["resources"]) {
		r := Map(raw)
		health := String(At(r, "health", "status"))
		sync := String(r["status"])
		_, statusPresent := r["status"]
		// Argo CD 3.5.1 deliberately omits sync status for lifecycle hooks
		// (controller/state.go). Only that absent field is non-blocking; an
		// explicit bad/null/empty status or unhealthy hook still blocks.
		syncReady := sync == "Synced" || !statusPresent && r["hook"] == true
		if !syncReady || health != "" && health != "Healthy" {
			version := String(r["version"])
			if group := String(r["group"]); group != "" {
				version = group + "/" + version
			}
			f.BlockingResources = append(f.BlockingResources, Ref{version, String(r["kind"]), String(r["namespace"]), String(r["name"])})
		}
	}
	// A hook's execution outcome belongs to operationState, not the resource
	// comparison row. Do not let an overall success hide contradictory hook
	// evidence. Ordinary resources may also have hookPhase=Running, so only
	// results explicitly identified as hooks participate here.
	for _, raw := range Slice(At(op, "syncResult", "resources")) {
		r := Map(raw)
		if _, present := r["hookType"]; !present {
			continue
		}
		switch String(r["hookType"]) {
		case "PreSync", "Sync", "PostSync", "SyncFail", "Skip":
		default:
			f.fail(Unknown, "HOOK_TYPE_UNKNOWN")
			continue
		}
		switch String(r["hookPhase"]) {
		case "Succeeded":
		case "Running", "Terminating":
			f.fail(Progressing, "HOOK_OPERATION_ACTIVE")
		case "Failed", "Error":
			f.fail(Degraded, "HOOK_OPERATION_FAILED")
		default:
			f.fail(Unknown, "HOOK_OUTCOME_UNKNOWN")
		}
	}
	SortedRefs(f.BlockingResources)
	if len(f.BlockingResources) > 0 {
		f.fail(Progressing, "BLOCKING_RESOURCES")
	}
	return f
}
