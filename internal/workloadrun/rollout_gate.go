package workloadrun

import (
	"atlas-refactor/internal/observation"
	"atlas-refactor/internal/platformnames"
	"atlas-refactor/internal/workload"
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Gate decisions describe a bounded read policy, not synthetic observation facts.
// Waiting never authorizes a dependent publication or a functional probe.
type GateState string

const (
	gateReady    GateState = "Ready"
	gateWaiting  GateState = "Waiting"
	gateRejected GateState = "Rejected"
)

type GateDecision struct {
	State   GateState `json:"state"`
	Reasons []string  `json:"reasons,omitempty"`
}

func (d GateDecision) err() error {
	if d.State == gateReady {
		return nil
	}
	message := strings.Join(d.Reasons, "; ")
	if d.State == gateWaiting {
		return Pending(message)
	}
	return errors.New(message)
}

// A session is private to one finite wait. Only the receipt-bound contract may
// enable transitions; knowledge (UID, target content, first comparison) grows
// monotonically. It is neither persisted as a resume cursor nor used for writes.
type rolloutSession struct {
	active     bool
	prior      map[string]Object
	owners     map[string]string
	accepted   map[string]bool
	uid        map[string]string
	targetSeen map[string]bool
	compared   map[string]bool
}

func newRolloutSession(active bool, prior map[string]Object, owners, uid map[string]string, accepted map[string]bool) *rolloutSession {
	pinned := map[string]string{}
	for id, v := range uid {
		pinned[id] = v
	}
	return &rolloutSession{active: active, prior: prior, owners: owners, uid: pinned, accepted: accepted, targetSeen: map[string]bool{}, compared: map[string]bool{}}
}
func appIdentity(name string) string {
	return "argoproj.io/Application/" + platformnames.Argo + "/" + name
}
func sameSpec(a, b Object) bool {
	return bytes.Equal(workload.JSON(a["spec"]), workload.JSON(b["spec"]))
}
func (s *rolloutSession) pin(id, uid string) error {
	if uid == "" {
		return errors.New("identity evidence missing: " + id)
	}
	if old := s.uid[id]; old != "" && old != uid {
		return errors.New("UID changed: " + id)
	}
	s.uid[id] = uid
	return nil
}

// Evaluate the whole Application snapshot before returning. Rejected always
// wins over Waiting, regardless of map order; raw S1 facts remain untouched.
func (s *rolloutSession) applications(expected []observation.ExpectedApplication, live map[string]Object) ([]observation.ApplicationFact, GateDecision) {
	expected = append([]observation.ExpectedApplication(nil), expected...)
	sort.Slice(expected, func(i, j int) bool { return expected[i].Name < expected[j].Name })
	facts := []observation.ApplicationFact{}
	wanted := map[string]bool{}
	rejected, waiting := []string{}, []string{}
	for _, want := range expected {
		wanted[want.Name] = true
		want.UID = s.uid[appIdentity(want.Name)]
		fact := observation.ClassifyApplication(want, live[want.Name], nil)
		facts = append(facts, fact)
		state, reason := s.application(want, fact, live[want.Name])
		if state == gateRejected {
			rejected = append(rejected, "Application "+want.Name+": "+reason)
		}
		if state == gateWaiting {
			waiting = append(waiting, "Application "+want.Name+": "+reason)
		}
	}
	for name := range live {
		if !wanted[name] {
			rejected = append(rejected, "unexpected Application: "+name)
		}
	}
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return facts, GateDecision{gateRejected, rejected}
	}
	if len(waiting) > 0 {
		return facts, GateDecision{gateWaiting, waiting}
	}
	return facts, GateDecision{State: gateReady}
}
func (s *rolloutSession) application(want observation.ExpectedApplication, fact observation.ApplicationFact, live Object) (GateState, string) {
	id := appIdentity(want.Name)
	introduced := s.active && s.prior[id] == nil
	if live == nil {
		if introduced && s.uid[id] == "" {
			return gateWaiting, "AWAITING_CREATION"
		}
		return gateRejected, "EXISTING_APPLICATION_ABSENT"
	}
	// This check is independent of status health, and applies even during creation.
	invariant := observation.ClassifyResource(observation.ExpectedResource{Ref: fact.Ref, UID: s.uid[id], Tracking: observation.Tracking(s.owners[want.Name], platformnames.Argo, fact.Ref), RequireSSA: true, Readiness: "identity-content"}, live, nil)
	if s.owners[want.Name] == "" || invariant.Classification != observation.Verified || fact.Generation == nil || *fact.Generation < 1 {
		return gateRejected, fmt.Sprintf("IDENTITY_OR_OWNERSHIP_INVALID %v", invariant.Reasons)
	}
	if !emptyListField(mapping(live["metadata"]), "finalizers") || !emptyListField(mapping(live["metadata"]), "ownerReferences") {
		return gateRejected, "APPLICATION_DELETION_BOUNDARY"
	}
	if err := s.pin(id, fact.UID); err != nil {
		return gateRejected, "UID_CHANGED"
	}
	target := bytes.Equal(workload.JSON(want.Spec), workload.JSON(live["spec"]))
	if target {
		s.targetSeen[id] = true
	} else if !s.active || s.prior[id] == nil || s.targetSeen[id] || !sameSpec(s.prior[id], live) {
		return gateRejected, "APPLICATION_SPEC_DRIFT"
	}
	if !validApplicationStatus(live) {
		return gateRejected, "MALFORMED_APPLICATION_STATUS"
	}
	if len(fact.Conditions) > 0 {
		return gateRejected, fmt.Sprintf("APPLICATION_ERROR %v", fact.Reasons)
	}
	if fact.Operation.Phase == "Failed" || fact.Operation.Phase == "Error" || fact.Operation.Phase == "Terminating" || fact.Health == "Degraded" {
		return gateRejected, "APPLICATION_FAILED_OR_TERMINATING"
	}
	if !fullSHA.MatchString(fact.ObservedRevision) {
		if introduced && !s.compared[id] && target && initialStatus(live) {
			return gateWaiting, "AWAITING_FIRST_COMPARISON"
		}
		return gateRejected, "REVISION_EVIDENCE_MISSING_OR_LOST"
	}
	if !s.accepted[fact.ObservedRevision] {
		return gateRejected, "UNPLANNED_REVISION"
	}
	s.compared[id] = true
	// Evaluate progress against the known predecessor spec without changing the
	// fact recorded against the target. No third spec may enter this path.
	progress := fact
	if !target {
		prior := want
		prior.Spec = mapping(s.prior[id]["spec"])
		progress = observation.ClassifyApplication(prior, live, nil)
	}
	switch progress.Classification {
	case observation.Verified:
		if !target {
			return gateWaiting, "AWAITING_TARGET_SPEC"
		}
		if prior := s.prior[id]; prior != nil && (!bytes.Equal(workload.JSON(at(prior, "spec", "source")), workload.JSON(want.Spec["source"])) || !bytes.Equal(workload.JSON(at(prior, "spec", "destination")), workload.JSON(want.Spec["destination"]))) {
			compared := mapping(at(live, "status", "sync", "comparedTo"))
			agrees := func(spec Object) bool {
				return bytes.Equal(workload.JSON(compared["source"]), workload.JSON(spec["source"])) && bytes.Equal(workload.JSON(compared["destination"]), workload.JSON(spec["destination"]))
			}
			if !agrees(want.Spec) {
				if s.active && agrees(mapping(prior["spec"])) {
					return gateWaiting, "AWAITING_TARGET_COMPARISON"
				}
				return gateRejected, "COMPARISON_SOURCE_DRIFT"
			}
		}
		return gateReady, ""
	case observation.Progressing:
		return gateWaiting, fmt.Sprintf("RECONCILING %v", progress.Reasons)
	default:
		return gateRejected, fmt.Sprintf("%s %v", progress.Classification, progress.Reasons)
	}
}
func emptyListField(o Object, key string) bool {
	v, exists := o[key]
	if !exists {
		return true
	}
	a, ok := v.([]any)
	return ok && len(a) == 0
}

// Check the types consumed by S1 before its permissive map accessors. A malformed
// collection or explicit null cannot become an empty collection and hide errors.
func validApplicationStatus(live Object) bool {
	raw, exists := live["status"]
	if !exists {
		return true
	}
	status, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"sync", "health", "operationState"} {
		if v, exists := status[key]; exists {
			o, ok := v.(map[string]any)
			if !ok {
				return false
			}
			for _, field := range []string{"status", "revision", "phase", "finishedAt"} {
				if value, exists := o[field]; exists {
					if _, ok := value.(string); !ok {
						return false
					}
				}
			}
		}
	}
	for _, key := range []string{"conditions", "resources"} {
		if v, exists := status[key]; exists {
			xs, ok := v.([]any)
			if !ok {
				return false
			}
			for _, x := range xs {
				o, ok := x.(map[string]any)
				if !ok {
					return false
				}
				if key == "conditions" && str(o["type"]) == "" {
					return false
				}
				if key == "resources" {
					if value, exists := o["status"]; exists {
						if _, ok := value.(string); !ok {
							return false
						}
					}
					if value, exists := o["hook"]; exists {
						if _, ok := value.(bool); !ok {
							return false
						}
					}
					if value, exists := o["health"]; exists {
						health, ok := value.(map[string]any)
						if !ok {
							return false
						}
						if value, exists := health["status"]; exists {
							if _, ok := value.(string); !ok {
								return false
							}
						}
					}
				}
			}
		}
	}
	if v, exists := live["operation"]; exists {
		if o, ok := v.(map[string]any); !ok || len(o) == 0 {
			return false
		}
	}
	return true
}

// Supported Argo initialization has no completed comparison/operation/history.
// This is a bounded allow-list for a newly introduced object, never a general
// conversion of Unknown to Waiting. Missing and partial initial status both work.
func initialStatus(live Object) bool {
	if _, exists := live["operation"]; exists {
		return false
	}
	for key, value := range mapping(live["status"]) {
		switch key {
		case "conditions", "resources":
			if !emptyListField(mapping(live["status"]), key) {
				return false
			}
		case "summary":
			fields, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for field := range fields {
				if (field != "images" && field != "externalURLs") || !emptyListField(fields, field) {
					return false
				}
			}
		case "sync", "health":
			fields, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for field, v := range fields {
				if field == "status" {
					text, ok := v.(string)
					if !ok {
						return false
					}
					if key == "sync" && text != "" && text != "Unknown" && text != "OutOfSync" {
						return false
					}
					if key == "health" && text != "" && text != "Unknown" && text != "Missing" && text != "Progressing" {
						return false
					}
				} else if field == "comparedTo" && key == "sync" {
					if !emptyComparison(v) {
						return false
					}
				} else if field == "message" && key == "health" {
					if _, ok := v.(string); !ok {
						return false
					}
				} else if field == "lastTransitionTime" && key == "health" {
					text, ok := v.(string)
					if !ok {
						return false
					}
					if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
						return false
					}
				} else if field == "revision" && key == "sync" {
					if v != "" {
						return false
					}
				} else {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func (s *rolloutSession) content(id string, want, live Object) error {
	if matches(want, live) {
		s.targetSeen[id] = true
		return nil
	}
	if s.active && !s.targetSeen[id] && s.prior[id] != nil && matches(s.prior[id], live) {
		return Pending("resource awaiting target content: " + id)
	}
	return errors.New("resource content drift: " + id)
}

// SyncStatus.ComparedTo is a value struct in Argo 3.5.1. Its zero value can
// serialize as nested empty objects before a comparison has supplied any source.
func emptyComparison(value any) bool {
	fields, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range fields {
		if key != "source" && key != "destination" {
			return false
		}
		part, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range part {
			allowed := key == "source" && (k == "repoURL" || k == "path" || k == "targetRevision") || key == "destination" && (k == "server" || k == "namespace" || k == "name")
			if !allowed || v != "" {
				return false
			}
		}
	}
	return true
}
