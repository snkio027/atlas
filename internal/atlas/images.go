package atlas

import (
	"context"
	"errors"
	"github.com/snkio027/atlas/internal/oci"
	"os"
	"path/filepath"
	"strings"
)

// Import only the supported node platform while retaining the original index
// digest. Docker's containerd store may export an index with absent platforms;
// kind load docker-image imports --all-platforms and fails on those entries.
// The archive is streamed from a private temporary file, never held in memory.
func (a *App) loadNodeImage(ctx context.Context, image string) error {
	if a.Config.fourNodeProfile() {
		path, e := a.imageArchive(image)
		if e != nil {
			return e
		}
		if e = oci.Verify(path, image); e != nil {
			return e
		}
		for _, node := range a.nodeNames() {
			if e = a.importNodeImage(ctx, image, path, node); e != nil {
				return e
			}
		}
		return nil
	}
	dir, e := safePath(a.Root, ".state")
	if e != nil {
		return e
	}
	archive, e := os.CreateTemp(dir, "image-*.tar")
	if e != nil {
		return e
	}
	path := archive.Name()
	defer os.Remove(path)
	if e = archive.Close(); e != nil {
		return e
	}
	if _, e = a.run(ctx, "docker", "image", "save", "--output", path, image); e != nil {
		return e
	}
	for _, node := range a.nodeNames() {
		if e := a.importNodeImage(ctx, image, path, node); e != nil {
			return e
		}
	}
	return nil
}

func (a *App) importNodeImage(ctx context.Context, image, path, node string) error {
	args := []string{"exec", "--privileged", "--interactive", node, "ctr", "--namespace", "k8s.io", "images", "import", "--platform", "linux/arm64", "--digests", "--snapshotter", "overlayfs", "-"}
	input, e := filepath.Rel(a.Root, path)
	if e != nil {
		return e
	}
	if _, e = a.Runner.Run(ctx, Request{Tool: "docker", Args: args, InputPath: input}); e != nil {
		return e
	}
	_, wantDigest, ok := strings.Cut(image, "@")
	if !ok {
		return errors.New("node import requires a digest-pinned image")
	}
	prefix := []string{"exec", node, "ctr", "--namespace", "k8s.io", "images"}
	listArgs := append(append([]string{}, prefix...), "list", "--quiet", "target.digest=="+wantDigest)
	refs, e := a.run(ctx, "docker", listArgs...)
	if e != nil {
		return e
	}
	sources := strings.Fields(string(refs))
	if len(sources) == 0 || strings.HasPrefix(sources[0], "-") {
		return errors.New("locked image digest absent after offline node import")
	}
	for _, ref := range sources {
		if ref == image {
			return nil
		}
	}
	if _, e = a.run(ctx, "docker", append(prefix, "tag", sources[0], image)...); e != nil {
		return e
	}
	refs, e = a.run(ctx, "docker", listArgs...)
	if e != nil {
		return e
	}
	for _, ref := range strings.Fields(string(refs)) {
		if ref == image {
			return nil
		}
	}
	return errors.New("exact locked image reference absent after offline node import")
}
