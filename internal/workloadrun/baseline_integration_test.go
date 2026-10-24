package workloadrun

import (
	"bytes"
	"context"
	"github.com/snkio027/atlas/internal/workload"
	"os"
	"testing"
	"time"
)

// Uses the existing plan's exact D1 installation. This opt-in check performs
// only GETs, local compilation and Git reads: no credential use, publication,
// authority evidence writes, image import or probe execution.
func TestS2ReadOnlyBaseline(t *testing.T) {
	config := os.Getenv("ATLAS_S2_BASELINE_CONFIG")
	if config == "" {
		t.Skip("opt-in read-only baseline")
	}
	w, err := Load(config)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := w.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err = w.LoadBase(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := w.ReadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if p.ClusterUID != w.Install.Record.ClusterUID || p.InstallID != w.Install.Record.InstallID {
		t.Fatal("foreign plan")
	}
	ids, err := w.readBaseline(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if before, e := w.baselineUIDs(); e == nil {
		if !bytes.Equal(workload.JSON(before), workload.JSON(ids)) {
			t.Fatal("existing baseline resource UID changed")
		}
	} else if !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if len(ids) == 0 {
		t.Fatal("empty baseline")
	}
	t.Logf("VERIFIED: %d resource identities, content, tracking and Argo SSA; cluster %s", len(ids), p.ClusterUID)
}
