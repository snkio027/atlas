package ot1

import (
	"errors"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
)

// A cooperative O_EXCL guard serializes handoffs. Normal runs cannot enter
// because the run lock exists throughout. Every crash leaves a lock or guard;
// uncertainty is deliberately not cleaned automatically.
func handoffLock(path string, old, next []byte, attempt string) error {
	guard := path + ".handoff"
	if e := observation.CreatePrivate(guard, observation.Bytes(observation.Object{"oldSHA256": observation.SHA(old), "nextSHA256": observation.SHA(next), "attempt": attempt})); e != nil {
		return e
	}
	current, e := observation.RegularPrivate(path)
	if e != nil || !bytesEqual(current, old) {
		return errors.New("predecessor lock changed before handoff")
	}
	original, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if e = observation.CreatePrivate(filepath.Join(attempt, "predecessor-lock.json"), old); e != nil {
		return e
	}
	pending := path + ".next"
	if e = observation.CreatePrivate(pending, next); e != nil {
		return e
	}
	current, e = observation.RegularPrivate(path)
	info, statErr := os.Lstat(path)
	if e != nil || statErr != nil || !os.SameFile(original, info) || !bytesEqual(current, old) {
		return errors.New("lock CAS failed")
	}
	if e = os.Rename(pending, path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	e = dir.Sync()
	_ = dir.Close()
	if e != nil {
		return e
	}
	return os.Remove(guard)
}
