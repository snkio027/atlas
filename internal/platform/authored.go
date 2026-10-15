package platform

import (
	"atlas-refactor/internal/authored"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Historical input keys remain stable in immutable bundles. Only the checkout
// authoring surface changes; locks and baseline evidence remain literal JSON.
func readInput(path string) ([]byte, error) {
	if strings.Contains(filepath.ToSlash(path), inputDir+"/") && strings.HasSuffix(path, ".json") && !strings.Contains(filepath.Base(path), "lock") && !strings.HasPrefix(filepath.Base(path), "baseline") && !strings.Contains(filepath.Base(path), "scope") && !strings.HasSuffix(path, "-images.json") {
		return authored.Read(path)
	}
	return os.ReadFile(path)
}
func (p *Project) inputPath(path string) (string, error) {
	resolved, err := authored.Resolve(filepath.Join(p.Root, path))
	if err != nil {
		return "", err
	}
	// Helm must see the same restricted data language as the typed readers.
	// Validate before invoking it, rather than accepting aliases/duplicates and
	// discovering the mismatch later while assembling the product bundle.
	if _, err = authored.Read(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}
func (p *Project) currentLayout() bool { return p.Config.Schema == 2 }

// Helm's YAML reader treats some plain strings (on/no) as booleans. Feed it
// normalized JSON values so authoring, hashing and rendering share one meaning.
func (p *Project) helmTemplate(ctx context.Context, args []string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "atlas-helm-values-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	args = append([]string(nil), args...)
	for i := 0; i < len(args); i++ {
		if args[i] != "--values" {
			continue
		}
		if i+1 == len(args) {
			return nil, fmt.Errorf("missing Helm values input")
		}
		i++
		raw, err := authored.Read(args[i])
		if err != nil {
			return nil, err
		}
		name := filepath.Join(dir, fmt.Sprintf("%d.json", i))
		if err = os.WriteFile(name, raw, 0600); err != nil {
			return nil, err
		}
		args[i] = name
	}
	return p.run(ctx, nil, p.Tools.Helm, args...)
}
