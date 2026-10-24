package workloadrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/installation"
	"github.com/snkio027/atlas/internal/oci"
	"github.com/snkio027/atlas/internal/workload"
	"path/filepath"
	"slices"
	"strings"
)

func (w *Workflow) imageArchive() ([]byte, string, error) {
	raw, e := regular(w.Config.ImageArchive, true)
	if e != nil {
		return nil, "", e
	}
	if workload.Digest(raw) != w.Config.ImageSHA256 {
		return nil, "", errors.New("OCI archive digest mismatch")
	}
	if len(w.Model.Intent.Workloads) == 0 {
		return nil, "", errors.New("missing workload")
	}
	ref := w.Model.Intent.Workloads[0].Image
	if !w.application() && !strings.HasPrefix(ref, "atlas.local/s2-web:v1@sha256:") {
		return nil, "", errors.New("local slice requires the verified Web OCI artifact")
	}
	for _, v := range w.Model.Intent.Workloads {
		if v.Image != ref {
			return nil, "", errors.New("slice Workloads must use the same reviewed image")
		}
	}
	if w.application() {
		if e = oci.VerifyApplication(w.Config.ImageArchive, ref); e != nil {
			return nil, "", e
		}
	}
	if e = oci.Verify(w.Config.ImageArchive, ref); e != nil {
		return nil, "", e
	}
	return raw, ref, nil
}

func (w *Workflow) ImportImage(ctx context.Context, p Plan, approval string) error {
	if e := w.approve(p, approval); e != nil {
		return e
	}
	if e := w.bind(ctx); e != nil {
		return e
	}
	if e := w.authority(ctx); e != nil {
		return e
	}
	b, ref, e := w.imageArchive()
	if e != nil {
		return e
	}
	if e = installation.VerifyTools(w.Install.Config.StateDirectory, w.Install.Product.Tools); e != nil {
		return e
	}
	nodes := []string{p.Cluster + "-control-plane", p.Cluster + "-worker", p.Cluster + "-worker2", p.Cluster + "-worker3"}
	run := func(input []byte, args ...string) ([]byte, error) {
		return command(ctx, w.Config.StateDirectory, input, "docker", append([]string{"--context", "orbstack"}, args...)...)
	}
	for _, node := range nodes {
		if e = importWebImage(run, node, b, ref); e != nil {
			return e
		}
	}
	return save(filepath.Join(w.Config.StateDirectory, "authority", approval, "image.json"), workload.JSON(Object{"archiveSHA256": w.Config.ImageSHA256, "reference": ref, "nodes": nodes}), true)
}

// importWebImage registers the canonical name that CRI actually resolves.
// ParseDockerRef strips the tag from repo:tag@digest; registering only the
// literal authored name in containerd leaves kubelet unable to find the image.
// The authored image and archive remain unchanged. Only the validated bounded
// repository:tag@sha256 form is supported; this is not a registry puller.
func importWebImage(run func([]byte, ...string) ([]byte, error), node string, archive []byte, ref string) error {
	tag, canonical, digest, err := oci.Reference(ref)
	if err != nil {
		return err
	}
	if _, e := run(archive, "exec", "-i", node, "ctr", "--namespace", "k8s.io", "images", "import", "--digests", "-"); e != nil {
		return fmt.Errorf("node %s image import: %w", node, e)
	}
	// Bind the tagging source to the verified manifest, not just a mutable tag.
	refs, e := run(nil, "exec", node, "ctr", "--namespace", "k8s.io", "images", "list", "--quiet", "target.digest==sha256:"+digest)
	if e != nil {
		return fmt.Errorf("node %s image digest lookup: %w", node, e)
	}
	matching := strings.Fields(string(refs))
	if !slices.Contains(matching, tag) {
		return fmt.Errorf("node %s imported tag does not identify the locked manifest", node)
	}
	if !slices.Contains(matching, canonical) {
		// No --force: an existing conflicting canonical name must fail closed.
		if _, e = run(nil, "exec", node, "ctr", "--namespace", "k8s.io", "images", "tag", tag, canonical); e != nil {
			return fmt.Errorf("node %s canonical image registration: %w", node, e)
		}
	}
	// Query the original authored reference, exactly as kubelet will. A tag-only
	// lookup or another repository with the same digest is not sufficient proof.
	raw, e := run(nil, "exec", node, "crictl", "inspecti", ref)
	if e != nil {
		return fmt.Errorf("node %s CRI image inspection: %w", node, e)
	}
	var info Object
	if e = json.Unmarshal(raw, &info); e != nil {
		return fmt.Errorf("node %s malformed CRI image evidence: %w", node, e)
	}
	for _, d := range array(at(info, "status", "repoDigests")) {
		if str(d) == canonical {
			return nil
		}
	}
	return fmt.Errorf("node %s canonical CRI image identity not proven", node)
}
