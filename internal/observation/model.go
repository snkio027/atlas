// Package observation reads and classifies facts. It has no mutation interface,
// watcher, desired-state writer or automatic recovery path.
package observation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Object = map[string]any

type Classification string

const (
	Verified    Classification = "VERIFIED"
	Progressing Classification = "PROGRESSING"
	Degraded    Classification = "DEGRADED"
	Drifted     Classification = "DRIFTED"
	Unknown     Classification = "UNKNOWN"
	Absent      Classification = "ABSENT"
)

type Ref struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

func (r Ref) Key() string { return r.APIVersion + "/" + r.Kind + "/" + r.Namespace + "/" + r.Name }
func (r Ref) Validate() error {
	name := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}$`)
	if !name.MatchString(r.Name) || (r.Namespace != "" && !name.MatchString(r.Namespace)) || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,100}$`).MatchString(r.Kind) || !regexp.MustCompile(`^(?:[a-z0-9][a-z0-9.-]*/)?v[0-9][a-z0-9]*$`).MatchString(r.APIVersion) {
		return errors.New("invalid resource identity")
	}
	return nil
}
func Reference(o Object) Ref {
	m := Map(o["metadata"])
	return Ref{String(o["apiVersion"]), String(o["kind"]), String(m["namespace"]), String(m["name"])}
}
func Map(v any) Object    { x, _ := v.(map[string]any); return x }
func String(v any) string { x, _ := v.(string); return x }
func Slice(v any) []any   { x, _ := v.([]any); return x }
func At(o Object, keys ...string) any {
	var v any = o
	for _, k := range keys {
		v = Map(v)[k]
	}
	return v
}
func Bytes(v any) []byte {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		panic(e)
	}
	return append(b, '\n')
}
func SHA(b []byte) string   { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Digest(v any) string   { return SHA(Bytes(v)) }
func FullSHA(s string) bool { return regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(s) }
func Hash(s string) bool    { return regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(s) }

// Decode refuses duplicate keys at every depth. Server objects allow additional
// fields; local contracts use strict=true to reject unknown schema fields too.
func Decode(b []byte, target any, strict bool) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				t, e = d.Token()
				if e != nil {
					return e
				}
				k, ok := t.(string)
				if !ok || seen[k] {
					return errors.New("duplicate/invalid JSON key")
				}
				seen[k] = true
				if e = walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case json.Delim('['):
			for d.More() {
				if e = walk(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON input")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if strict {
		d.DisallowUnknownFields()
	}
	return d.Decode(target)
}
func Clone(o Object) Object { var r Object; _ = Decode(Bytes(o), &r, false); return r }

const TrackingAnnotation = "argocd.argoproj.io/tracking-id"

// Semantic retains every non-runtime field, including policy, other annotations,
// finalizers and ownerReferences. UID and tracking are separate evidence fields.
func Semantic(o Object) Object {
	r := Clone(o)
	delete(r, "status")
	m := Map(r["metadata"])
	for _, k := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields"} {
		delete(m, k)
	}
	a := Map(m["annotations"])
	delete(a, TrackingAnnotation)
	if len(a) == 0 {
		delete(m, "annotations")
	}
	return r
}
func Tracking(owner, destination string, ref Ref) string {
	group, _, ok := strings.Cut(ref.APIVersion, "/")
	if !ok {
		group = ""
	}
	ns := ref.Namespace
	if ns == "" {
		ns = destination
	}
	return owner + ":" + group + "/" + ref.Kind + ":" + ns + "/" + ref.Name
}

type Target struct {
	Cluster          string `json:"cluster"`
	Context          string `json:"context"`
	ClusterUID       string `json:"clusterUID"`
	KubeconfigSHA256 string `json:"kubeconfigSHA256"`
}

func (t Target) Validate() error {
	if !regexp.MustCompile(`^atlas-refactor-test(?:-[a-z0-9]{1,12})?$`).MatchString(t.Cluster) || t.Context != "kind-"+t.Cluster || t.ClusterUID == "" || !Hash(t.KubeconfigSHA256) {
		return errors.New("explicit isolated target binding is required")
	}
	return nil
}

type ExpectedResource struct {
	Ref            Ref    `json:"ref"`
	UID            string `json:"uid,omitempty"`
	SemanticSHA256 string `json:"semanticSHA256,omitempty"`
	Tracking       string `json:"tracking,omitempty"`
	RequireSSA     bool   `json:"requireSSA,omitempty"`
	// identity-content is deliberately not a generic resource health assertion.
	Readiness string `json:"readiness"`
}
type ExpectedApplication struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	Spec      Object `json:"spec"`
	Revision  string `json:"revision"`
}
type Expectation struct {
	Schema            int                   `json:"schema"`
	Subject           string                `json:"subject"`
	Target            Target                `json:"target"`
	ImplementationSHA string                `json:"implementationSHA"`
	Revision          string                `json:"revision"`
	Applications      []ExpectedApplication `json:"applications"`
	Resources         []ExpectedResource    `json:"resources"`
}

func (e Expectation) Validate() error {
	if e.Schema != 1 || e.Subject == "" || !FullSHA(e.ImplementationSHA) || !FullSHA(e.Revision) {
		return errors.New("incomplete observation source binding")
	}
	if err := e.Target.Validate(); err != nil {
		return err
	}
	if len(e.Applications) == 0 && len(e.Resources) == 0 {
		return errors.New("empty observation expectation")
	}
	seen := map[string]bool{}
	for _, a := range e.Applications {
		r := Ref{"argoproj.io/v1alpha1", "Application", a.ApplicationNamespace(), a.Name}
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.Key()] || !FullSHA(a.Revision) || len(a.Spec) == 0 {
			return errors.New("invalid/duplicate Application expectation")
		}
		seen[r.Key()] = true
	}
	for _, r := range e.Resources {
		if err := r.Ref.Validate(); err != nil {
			return err
		}
		if seen[r.Ref.Key()] || r.Ref.Kind == "Secret" || r.Ref.Kind == "SealedSecret" || r.Ref.Kind == "Application" {
			return errors.New("duplicate or sensitive/ambiguous observation resource")
		}
		seen[r.Ref.Key()] = true
		if r.SemanticSHA256 != "" && !Hash(r.SemanticSHA256) {
			return errors.New("invalid content digest")
		}
		switch r.Readiness {
		case "identity-content", "namespace-active", "deployment-available", "crd-established", "pvc-bound":
		default:
			return errors.New("unknown readiness rule")
		}
		if r.Readiness == "namespace-active" && (r.Ref.APIVersion != "v1" || r.Ref.Kind != "Namespace") || r.Readiness == "deployment-available" && (r.Ref.APIVersion != "apps/v1" || r.Ref.Kind != "Deployment") || r.Readiness == "crd-established" && (r.Ref.APIVersion != "apiextensions.k8s.io/v1" || r.Ref.Kind != "CustomResourceDefinition") || r.Readiness == "pvc-bound" && (r.Ref.APIVersion != "v1" || r.Ref.Kind != "PersistentVolumeClaim") {
			return errors.New("readiness rule does not match GVK")
		}
	}
	return nil
}

type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
}
type Operation struct {
	Active     bool   `json:"active"`
	Phase      string `json:"phase,omitempty"`
	Revision   string `json:"revision,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Info       []any  `json:"info,omitempty"`
}
type ResourceFact struct {
	Ref                Ref            `json:"ref"`
	UID                string         `json:"uid,omitempty"`
	ResourceVersion    string         `json:"resourceVersion,omitempty"`
	Generation         *int64         `json:"generation,omitempty"`
	ObservedGeneration *int64         `json:"observedGeneration,omitempty"`
	SemanticSHA256     string         `json:"semanticSHA256,omitempty"`
	Tracking           string         `json:"tracking,omitempty"`
	ArgoSSA            bool           `json:"argoSSA"`
	Conditions         []Condition    `json:"conditions,omitempty"`
	Classification     Classification `json:"classification"`
	Reasons            []string       `json:"reasons,omitempty"`
}
type ApplicationFact struct {
	ResourceFact
	ExpectedRevision   string    `json:"expectedRevision"`
	ObservedRevision   string    `json:"observedRevision,omitempty"`
	ExpectedSpecSHA256 string    `json:"expectedSpecSHA256"`
	ObservedSpecSHA256 string    `json:"observedSpecSHA256,omitempty"`
	Sync               string    `json:"sync,omitempty"`
	Health             string    `json:"health,omitempty"`
	Operation          Operation `json:"operation"`
	BlockingResources  []Ref     `json:"blockingResources,omitempty"`
}
type Envelope struct {
	ProofVersion       string            `json:"proofVersion,omitempty"`
	ClosingRaw         []Object          `json:"closingRaw,omitempty"`
	BinarySHA256       string            `json:"binarySHA256,omitempty"`
	LockedToolVersions map[string]string `json:"lockedToolVersions,omitempty"`
	Schema             string            `json:"schema"`
	Subject            string            `json:"subject"`
	Target             Target            `json:"target"`
	ImplementationSHA  string            `json:"implementationSHA"`
	ExpectedRevision   string            `json:"expectedRevision"`
	ExpectationSHA256  string            `json:"expectationSHA256"`
	StartedAt          time.Time         `json:"startedAt"`
	FinishedAt         time.Time         `json:"finishedAt"`
	Classification     Classification    `json:"classification"`
	Reasons            []string          `json:"reasons,omitempty"`
	Applications       []ApplicationFact `json:"applications"`
	Resources          []ResourceFact    `json:"resources"`
	// Raw is private evidence only, never emitted in the human report or Git.
	Raw []Object `json:"raw"`
}

func (e Envelope) ExitCode() int {
	if e.Classification == Verified {
		return 0
	}
	if e.Classification == Unknown || e.Classification == Drifted {
		return 2
	}
	return 1
}
func (e Envelope) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\ncluster=%s revision=%s\n", e.Subject, e.Classification, e.Target.Cluster, e.ExpectedRevision)
	for _, a := range e.Applications {
		fmt.Fprintf(&b, "Application/%s: %s revision=%s sync=%s health=%s active=%t [%s]\n", a.Ref.Name, a.Classification, a.ObservedRevision, a.Sync, a.Health, a.Operation.Active, strings.Join(a.Reasons, ","))
	}
	for _, r := range e.Resources {
		if r.Classification != Verified {
			fmt.Fprintf(&b, "%s: %s [%s]\n", r.Ref.Key(), r.Classification, strings.Join(r.Reasons, ","))
		}
	}
	if len(e.Reasons) > 0 {
		fmt.Fprintf(&b, "[%s]\n", strings.Join(e.Reasons, ","))
	}
	return b.String()
}
func SortedRefs(values []Ref) {
	sort.Slice(values, func(i, j int) bool { return values[i].Key() < values[j].Key() })
}

func (a ExpectedApplication) ApplicationNamespace() string {
	if a.Namespace != "" {
		return a.Namespace
	}
	return "argocd"
}
