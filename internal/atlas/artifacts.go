package atlas

import (
	"context"
	"fmt"
	"github.com/snkio027/atlas/internal/oci"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) imageArchive(image string) (string, error) {
	_, hash, ok := strings.Cut(image, "@sha256:")
	if !ok || len(hash) != 64 {
		return "", fmt.Errorf("unlocked image: %s", image)
	}
	return safePath(a.Root, ".state/images/"+hash+".tar")
}

// PrepareImages is called ONLY by the separate, explicitly online artifact
// preparation command. Normal Bootstrap never pulls or builds images.
func (a *App) PrepareImages(ctx context.Context) error {
	if e := a.VerifyArtifacts(); e != nil {
		return e
	}
	if e := a.verifyTools(ctx, true); e != nil {
		return e
	}
	release, e := a.acquire()
	if e != nil {
		return e
	}
	defer release()
	dir, e := safePath(a.Root, ".state/images")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	for _, image := range a.lockedImages() {
		path, e := a.imageArchive(image)
		if e != nil {
			return e
		}
		if _, e = a.run(ctx, "docker", "image", "inspect", "--platform", "linux/arm64", image); e != nil {
			if _, e = a.run(ctx, "docker", "pull", "--platform", "linux/arm64", image); e != nil {
				return e
			}
		}
		if oci.Verify(path, image) == nil {
			continue
		}
		tmp, e := os.CreateTemp(dir, "prepare-*.tar")
		if e != nil {
			return e
		}
		name := tmp.Name()
		_ = tmp.Close()
		prepare := func() error {
			if _, e = a.run(ctx, "docker", "image", "save", "--output", name, image); e != nil {
				return e
			}
			if e = oci.Verify(name, image); e != nil {
				// Docker's lazy store can export metadata without the local platform's
				// content. Materialize the locked FROM without RUN or a replacement tag.
				if _, e = a.Runner.Run(ctx, Request{Tool: "docker", Args: []string{"build", "--platform", "linux/arm64", "-"}, Input: []byte("FROM " + image + "\n")}); e != nil {
					return e
				}
				if _, e = a.run(ctx, "docker", "image", "save", "--output", name, image); e != nil {
					return e
				}
				if e = oci.Verify(name, image); e != nil {
					return fmt.Errorf("incomplete locked image %s: %w", image, e)
				}
			}
			return os.Rename(name, path)
		}
		e = prepare()
		_ = os.Remove(name)
		if e != nil {
			return e
		}
	}
	return nil
}

func (a *App) verifyImageArchives() error {
	for _, image := range a.lockedImages() {
		path, e := a.imageArchive(image)
		if e != nil {
			return e
		}
		if e = oci.Verify(path, image); e != nil {
			return fmt.Errorf("offline image archive %s: %w; run atlas-artifacts prepare first", filepath.Base(path), e)
		}
	}
	return nil
}
