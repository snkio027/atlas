package ot1

import (
	"context"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"github.com/snkio027/atlas/internal/observation"
	"os"
	"path/filepath"
	"testing"
)

func TestIsolatedProfileRealRender(t *testing.T) {
	tools := frozenRenderTools(t)
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(root, "platform/development/bootstrap/baseline-v3.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(base, 0700); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(base, "repo")
	result, e := PrepareRepository(context.Background(), root, target, tools)
	if e != nil {
		t.Fatal(e)
	}
	if !observation.FullSHA(result.Commit) || result.ClusterOperations != 0 || result.Target != developmentprofile.OT1Cluster {
		t.Fatal(result)
	}
	after, _ := os.ReadFile(filepath.Join(root, "platform/development/bootstrap/baseline-v3.json"))
	if observation.SHA(before) != observation.SHA(after) {
		t.Fatal("changed original snapshot")
	}
	c, l, e := atlas.Load(target, "profiles/ot1.json")
	if e != nil {
		t.Fatal(e)
	}
	app := atlas.App{Root: target, Config: c, Lock: l}
	if _, e = app.Render(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{CredentialInput, CredentialOutput} {
		o, e := readObject(target, path)
		if e != nil || len(observation.Slice(o["items"])) != 0 {
			t.Fatal("old ciphertext imported", e)
		}
	}
	if _, e = PrepareRepository(context.Background(), root, target, tools); e == nil {
		t.Fatal("reused preparation destination")
	}
	// Tampering cannot be repaired by computing a new local snapshot digest.
	path := filepath.Join(target, "platform/development/bootstrap/kind.json")
	b, _ := os.ReadFile(path)
	b = append(b, ' ')
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	app = atlas.App{Root: target, Config: c, Lock: l}
	if _, e = app.Render(context.Background()); e == nil {
		t.Fatal("changed frozen profile accepted")
	}
}
