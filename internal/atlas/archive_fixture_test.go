package atlas

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prepareFixtureArchives(t *testing.T, a *App) {
	t.Helper()
	blobs := map[string][]byte{}
	add := func(b []byte) Object {
		h := digest(b)
		blobs[h] = b
		return Object{"digest": "sha256:" + h, "size": len(b)}
	}
	config := add([]byte(`{"os":"linux","architecture":"arm64"}`))
	layer := add([]byte("test-layer"))
	manifest := add(jsonBytes(Object{"config": config, "layers": []Object{layer}}))
	manifest["platform"] = Object{"os": "linux", "architecture": "arm64"}
	index := add(jsonBytes(Object{"manifests": []Object{manifest}}))
	hash := index["digest"].(string)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	indexBytes := jsonBytes(Object{"manifests": []Object{index}})
	_ = tw.WriteHeader(&tar.Header{Name: "index.json", Mode: 0600, Size: int64(len(indexBytes))})
	_, _ = tw.Write(indexBytes)
	for h, b := range blobs {
		_ = tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + h, Mode: 0600, Size: int64(len(b))})
		_, _ = tw.Write(b)
	}
	_ = tw.Close()
	path := filepath.Join(a.Root, "platform/development/versions.lock.json")
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var lock Object
	_ = json.Unmarshal(b, &lock)
	for k, v := range lock["images"].(map[string]any) {
		ref, _, _ := strings.Cut(v.(string), "@")
		lock["images"].(map[string]any)[k] = ref + "@" + hash
	}
	images := lock["images"].(map[string]any)
	a.Lock.NodeImage = images["node"].(string)
	a.Lock.ArgoImage = images["argo"].(string)
	a.Lock.RedisImage = images["redis"].(string)
	if e = os.WriteFile(path, jsonBytes(lock), 0600); e != nil {
		t.Fatal(e)
	}
	// This simulator uses frozen dependencies and synthetic OCI archives.
	// Bind only its temporary snapshot to those synthetic inputs; never rewrite the
	// repository's historical snapshot or expose a runtime override.
	snapshotPath := filepath.Join(a.Root, "platform/development/bootstrap/baseline-v3.json")
	snapshotBytes, e := os.ReadFile(snapshotPath)
	if e != nil {
		t.Fatal(e)
	}
	var snapshot Object
	if e = json.Unmarshal(snapshotBytes, &snapshot); e != nil {
		t.Fatal(e)
	}
	for path := range snapshot["bundleHashes"].(map[string]any) {
		input := path
		switch path {
		case "gitops/platform/applications/overlays/development/resources.json":
			input = "platform/development/capabilities/core-applications.json"
		case "gitops/platform/management/projects/overlays/development/resources.json":
			input = "platform/development/capabilities/core-projects.json"
		}
		b, err := os.ReadFile(filepath.Join(a.Root, input))
		if err != nil {
			t.Fatal(err)
		}
		snapshot["bundleHashes"].(map[string]any)[path] = digest(b)
	}
	if e = os.WriteFile(snapshotPath, jsonBytes(snapshot), 0600); e != nil {
		t.Fatal(e)
	}
	a.fixtureSnapshotDigest = digest(jsonBytes(snapshot))
	if e = os.MkdirAll(filepath.Join(a.Root, ".state/images"), 0700); e != nil {
		t.Fatal(e)
	}
	archive, _ := a.imageArchive(a.Lock.NodeImage)
	if e = os.WriteFile(archive, buf.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestMissingOfflineArchivePreventsClusterCreation(t *testing.T) {
	a, s := developmentFixture(t)
	p, _ := a.imageArchive(a.Lock.NodeImage)
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e := a.Apply(t.Context(), a.Config.Cluster, true); e == nil {
		t.Fatal("accepted missing archive")
	}
	if len(s.effects) != 0 {
		t.Fatal(s.effects)
	}
}
