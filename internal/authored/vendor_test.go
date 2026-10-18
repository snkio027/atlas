package authored

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineVendorIntegrity(t *testing.T) {
	root := "../.."
	b, e := os.ReadFile(filepath.Join(root, "packaging/go-modules.lock.json"))
	if e != nil {
		t.Fatal(e)
	}
	var lock struct {
		Schema                         int
		Module, Version, Sum, GoModSum string
		Files                          map[string]string
	}
	if e = json.Unmarshal(b, &lock); e != nil {
		t.Fatal(e)
	}
	sums, e := os.ReadFile(filepath.Join(root, "go.sum"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(sums), lock.Module+" "+lock.Version+" "+lock.Sum) || !strings.Contains(string(sums), lock.Module+" "+lock.Version+"/go.mod "+lock.GoModSum) {
		t.Fatal("module provenance differs")
	}
	for name, want := range lock.Files {
		raw, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != want {
			t.Fatal("vendor content changed", name)
		}
	}
	e = filepath.WalkDir(filepath.Join(root, "vendor/go.yaml.in"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			name, _ := filepath.Rel(root, path)
			if lock.Files[name] == "" {
				t.Errorf("unlocked vendor source %s", name)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
