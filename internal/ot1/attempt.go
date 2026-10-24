package ot1

import (
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"time"
)

// Attempt records finite state progression. Its only side effect is create-only
// evidence on local disk. Application request execution remains a separate gate.
type Attempt struct {
	Plan               Plan
	Directory          string
	next               int
	baseline, previous *Snapshot
	stopped            bool
}
type Checkpoint struct {
	Schema         int        `json:"schema"`
	PlanSHA256     string     `json:"planSHA256"`
	Index          int        `json:"index"`
	Stage          string     `json:"stage"`
	SnapshotSHA256 string     `json:"snapshotSHA256"`
	PreviousSHA256 string     `json:"previousSHA256,omitempty"`
	Assessment     Assessment `json:"assessment"`
	RecordedAt     time.Time  `json:"recordedAt"`
}

func NewAttempt(plan Plan, directory string) (*Attempt, error) {
	if _, e := os.Lstat(directory); !os.IsNotExist(e) {
		return nil, errors.New("attempt exists; interrupted/failed attempts must be preserved")
	}
	if e := observation.PrivateDirectory(filepath.Dir(directory)); e != nil {
		return nil, e
	}
	if e := os.Mkdir(directory, 0700); e != nil {
		return nil, e
	}
	if e := observation.CreatePrivate(filepath.Join(directory, "plan.json"), observation.Bytes(plan)); e != nil {
		return nil, e
	}
	return &Attempt{Plan: plan, Directory: directory}, nil
}
func (a *Attempt) Record(snapshot Snapshot, desired map[string]observation.Object, gate *GateProof) (Checkpoint, error) {
	if a.stopped || a.next >= len(a.Plan.Phases) {
		return Checkpoint{}, errors.New("attempt is terminal")
	}
	phase := a.Plan.Phases[a.next]
	c := Checkpoint{Schema: 1, PlanSHA256: observation.Digest(a.Plan), Index: a.next, Stage: phase.Stage.Name, SnapshotSHA256: observation.Digest(snapshot), RecordedAt: time.Now().UTC()}
	if a.previous != nil {
		c.PreviousSHA256 = observation.Digest(*a.previous)
	}
	c.Assessment = Assess(a.Plan, a.next, snapshot, a.baseline, a.previous, desired, gate)
	stem := fmt.Sprintf("%02d-%s", a.next, phase.Stage.Name)
	if e := observation.CreatePrivate(filepath.Join(a.Directory, stem+"-snapshot.json"), observation.Bytes(snapshot)); e != nil {
		a.stopped = true
		return c, e
	}
	if gate != nil {
		// Receipt references must resolve to owner-only files inside this attempt.
		// The raw outputs remain private and are never replaced by a later success.
		for name, hash := range gate.EvidenceFiles {
			if name != "status.json" && name != "repeat-apply.json" && name != "audit-before.json" && name != "audit-after.json" && name != "runtime.json" {
				a.stopped = true
				return c, errors.New("invalid gate evidence path")
			}
			b, e := observation.RegularPrivate(filepath.Join(a.Directory, stem+"-"+name))
			if e != nil || observation.SHA(b) != hash {
				c.Assessment.Atlas = "NOT_PROVEN"
				c.Assessment.Reasons = append(c.Assessment.Reasons, "GATE_EVIDENCE_UNAVAILABLE")
				break
			}
		}
		if e := observation.CreatePrivate(filepath.Join(a.Directory, stem+"-gate.json"), observation.Bytes(gate)); e != nil {
			a.stopped = true
			return c, e
		}
	}
	if e := observation.CreatePrivate(filepath.Join(a.Directory, stem+"-checkpoint.json"), observation.Bytes(c)); e != nil {
		a.stopped = true
		return c, e
	}
	if !c.Assessment.Passed() {
		a.stopped = true
		return c, a.terminal("STOP", c)
	}
	if a.next == 0 {
		var copy Snapshot
		_ = observation.Decode(observation.Bytes(snapshot), &copy, true)
		a.baseline = &copy
	}
	var copy Snapshot
	_ = observation.Decode(observation.Bytes(snapshot), &copy, true)
	a.previous = &copy
	a.next++
	if a.next == len(a.Plan.Phases) {
		a.stopped = true
		return c, a.terminal("VERIFIED", c)
	}
	return c, nil
}
func (a *Attempt) terminal(state string, c Checkpoint) error {
	return observation.CreatePrivate(filepath.Join(a.Directory, "terminal.json"), observation.Bytes(observation.Object{"state": state, "stage": c.Stage, "planSHA256": c.PlanSHA256, "checkpointSHA256": observation.Digest(c), "noAutomaticRecovery": true}))
}

// Stop records an unavailable read, timeout, interrupted action or any unexpected
// precondition as a permanent terminal outcome. It never submits a recovery write.
func (a *Attempt) Stop(reason string) error {
	if a.stopped {
		return errors.New("attempt already terminal")
	}
	a.stopped = true
	return observation.CreatePrivate(filepath.Join(a.Directory, "terminal.json"), observation.Bytes(observation.Object{"state": "STOP", "planSHA256": observation.Digest(a.Plan), "nextIndex": a.next, "reason": reason, "noAutomaticRecovery": true}))
}
func (a *Attempt) Next() (Step, error) {
	if a.stopped || a.next >= len(a.Plan.Phases) {
		return Step{}, errors.New("attempt is terminal")
	}
	return Steps(a.Plan)[a.next], nil
}
