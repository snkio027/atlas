package atlas

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/snkio027/atlas/internal/oci"
	"os"
	"path/filepath"
	"slices"
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
	_, canonical, digest, e := oci.Reference(image)
	if e != nil {
		return e
	}
	args := []string{"exec", "--privileged", "--interactive", node, "ctr", "--namespace", "k8s.io", "images", "import", "--platform", "linux/arm64", "--digests", "--snapshotter", "overlayfs", "-"}
	input, e := filepath.Rel(a.Root, path)
	if e != nil {
		return e
	}
	if _, e = a.Runner.Run(ctx, Request{Tool: "docker", Args: args, InputPath: input}); e != nil {
		return e
	}
	wantDigest := "sha256:" + digest
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
	// CRI strips the tag from repo:tag@digest. The authored alias alone is not
	// sufficient for kubelet to find an offline image, even when ctr lists it.
	for _, ref := range []string{image, canonical} {
		if !slices.Contains(sources, ref) {
			// Never replace a conflicting alias with --force.
			if _, e = a.run(ctx, "docker", append(prefix, "tag", sources[0], ref)...); e != nil {
				return e
			}
			sources = append(sources, ref)
		}
	}
	refs, e = a.run(ctx, "docker", listArgs...)
	if e != nil {
		return e
	}
	registered := strings.Fields(string(refs))
	if !slices.Contains(registered, image) || !slices.Contains(registered, canonical) {
		return errors.New("locked image aliases absent after offline node import")
	}
	raw, e := a.run(ctx, "docker", "exec", node, "crictl", "inspecti", image)
	if e != nil {
		return e
	}
	var info struct {
		Status struct {
			RepoDigests []string `json:"repoDigests"`
		} `json:"status"`
	}
	if e = json.Unmarshal(raw, &info); e != nil {
		return errors.New("invalid CRI image inspection response")
	}
	if !slices.Contains(info.Status.RepoDigests, canonical) {
		return errors.New("canonical CRI image identity not proven after offline import")
	}
	return nil
}
