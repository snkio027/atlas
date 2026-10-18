package managementaccess

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadTree accepts a local Git export; it never reads private installation
// state or follows symlinks. Only the GitOps tree is needed for compilation.
func ReadTree(dir string) (Files, error) {
	out := Files{}
	err := filepath.WalkDir(filepath.Join(dir, "gitops"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in deployment input")
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 32<<20 {
			return errors.New("invalid deployment input file")
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out, err
}

// WriteNew creates review artifacts only, never a deployment checkout. An
// existing directory is rejected even when its contents happen to match.
func WriteNew(dir string, files Files) error {
	for p := range files {
		if !filepath.IsLocal(p) || strings.Contains(p, "\\") {
			return errors.New("unsafe output path")
		}
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	for _, p := range sortedPaths(files) {
		b := files[p]
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(b)
		closeErr := f.Close()
		if err = errors.Join(writeErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}
