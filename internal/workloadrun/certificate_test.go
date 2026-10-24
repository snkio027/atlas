package workloadrun

import (
	"encoding/json"
	"errors"
	"github.com/snkio027/atlas/internal/workload"
	"testing"
)

func TestCertificatePrerequisiteUsesCurrentGeneration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		observed   any
		generation int64
		want       string
	}{
		{"issued", "True", 2, 2, "ready"},
		{"API decoded generation", "True", float64(2), 2, "ready"},
		{"stale Ready", "True", 1, 2, "waiting"},
		{"issuing", "False", 2, 2, "waiting"},
		{"unknown", "Unknown", 2, 2, "waiting"},
		{"missing observed generation", "True", nil, 2, "rejected"},
		{"zero observed generation", "True", 0, 2, "rejected"},
		{"future observed generation", "True", 3, 2, "rejected"},
		{"fractional observed generation", "True", 1.5, 2, "rejected"},
		{"string observed generation", "True", "2", 2, "rejected"},
		{"invalid status", "true", 2, 2, "rejected"},
		{"missing status", "", 2, 2, "rejected"},
		{"missing generation", "True", 2, 0, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := Object{"status": Object{"conditions": []any{Object{"type": "Ready", "status": tc.status, "observedGeneration": tc.observed}}}}
			checkCertificateGate(t, live, &tc.generation, tc.want)
		})
	}
}

func TestCertificatePrerequisiteRejectsAmbiguousProof(t *testing.T) {
	ready := Object{"type": "Ready", "status": "True", "observedGeneration": 2}
	for _, tc := range []struct {
		name       string
		conditions any
		want       string
	}{
		{"absent", nil, "waiting"},
		{"empty", []any{}, "waiting"},
		{"issuing only", []any{Object{"type": "Issuing", "status": "True", "observedGeneration": 2}}, "waiting"},
		{"additional condition", []any{Object{"type": "Issuing", "status": "False", "observedGeneration": 2}, ready}, "ready"},
		{"duplicate Ready", []any{ready, ready}, "rejected"},
		{"contradictory Ready", []any{ready, Object{"type": "Ready", "status": "False", "observedGeneration": 2}}, "rejected"},
		{"malformed collection", ready, "rejected"},
		{"malformed member", []any{"bad"}, "rejected"},
		{"null member", []any{nil}, "rejected"},
		{"missing type", []any{Object{"status": "True", "observedGeneration": 2}}, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generation := int64(2)
			live := Object{"status": Object{"conditions": tc.conditions}}
			checkCertificateGate(t, live, &generation, tc.want)
		})
	}
	checkCertificateGate(t, Object{}, nil, "rejected")
}

func checkCertificateGate(t *testing.T, live Object, generation *int64, want string) {
	t.Helper()
	// Exercise the same JSON number representation as kubectl reads.
	data := workload.JSON(live)
	var decoded Object
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	err := certificateReady(decoded, generation)
	got := "ready"
	if err != nil {
		var pending Pending
		got = "rejected"
		if errors.As(err, &pending) {
			got = "waiting"
		}
	}
	if got != want {
		t.Fatalf("want %s, got %s: %v", want, got, err)
	}
	if string(data) != string(workload.JSON(decoded)) {
		t.Fatal("readiness check mutated live evidence")
	}
}
