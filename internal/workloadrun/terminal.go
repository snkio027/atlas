package workloadrun

import (
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/workload"
	"os"
	"path/filepath"
)

// Inspect both markers before allowing a completed repeat. Historical PASS/STOP
// contradictions must be reported, never resolved by giving one marker priority.
func attemptComplete(dir string) (bool, error) {
	present := map[string]bool{}
	for _, name := range []string{"final.json", "terminal.json"} {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return false, fmt.Errorf("inspect %s: %w", name, err)
		}
		if _, err := regular(path, true); err != nil {
			return false, fmt.Errorf("read %s: %w", name, err)
		}
		present[name] = true
	}
	if present["final.json"] && present["terminal.json"] {
		return false, errors.New("contradictory attempt evidence: final.json and terminal.json both exist")
	}
	if present["terminal.json"] {
		return false, errors.New("prior attempt stopped; inspect external state before a new reviewed execution")
	}
	return present["final.json"], nil
}

func probeAvailable(dir string) error {
	complete, err := attemptComplete(dir)
	if err != nil {
		return err
	}
	if complete {
		return errors.New("probe already completed; use read-only observation")
	}
	if _, err = os.Lstat(filepath.Join(dir, "probe-started.json")); err == nil {
		return errors.New("probe intent already exists; outcome requires inspection, not replay")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Unlike save's idempotent evidence semantics, acquiring an existing intent is
// always an error, even for identical bytes. Keep partial files on persistence
// failure; uncertain execution must remain fenced. The CLI also holds the D1 lock.
func claimProbe(dir string, intent []byte) error {
	if err := probeAvailable(dir); err != nil {
		return err
	}
	// Consumer receipt already created this private directory.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return errors.New("unsafe probe intent directory")
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("probe intent directory must be owner-only")
	}
	parent, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open probe intent directory: %w", err)
	}
	defer parent.Close()
	f, err := os.OpenFile(filepath.Join(dir, "probe-started.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create probe intent: %w", err)
	}
	_, err = f.Write(intent)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = parent.Sync()
	}
	if err != nil {
		return fmt.Errorf("persist probe intent: %w", err)
	}
	return nil
}

func (w *Workflow) retainStop(approval string, cause error) error {
	dir := filepath.Join(w.Config.StateDirectory, "authority", approval)
	b, err := regular(filepath.Join(w.Config.StateDirectory, "latest-observation.json"), true)
	if err == nil {
		err = save(filepath.Join(dir, "stop-observation.json"), b, true)
	}
	if err != nil {
		cause = errors.Join(cause, fmt.Errorf("STOP evidence incomplete: retain stop-observation.json: %w", err))
	}
	if err = save(filepath.Join(dir, "terminal.json"), workload.JSON(Object{"result": "STOP", "planSHA256": approval, "reason": cause.Error(), "externalStateMayHaveChanged": true}), true); err != nil {
		cause = errors.Join(cause, fmt.Errorf("STOP evidence incomplete: retain terminal.json: %w", err))
	}
	return cause
}
