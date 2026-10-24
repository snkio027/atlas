package workloadrun

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/snkio027/atlas/internal/webslice"
	"github.com/snkio027/atlas/internal/workload"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The only bucket mutation denial test runs against a disposable container with
// freshly generated synthetic credentials and tmpfs data, never the D1 bucket.
func TestSeaweed447PolicyFixture(t *testing.T) {
	if os.Getenv("ATLAS_S2_PROVIDER_FIXTURE") != "1" {
		t.Skip("opt-in isolated Docker fixture")
	}
	image := "docker.io/chrislusf/seaweedfs:4.47@sha256:ce9e796f1fe6f06968f4c04bdaf8f678dad9c8acdfef3d244133d71bfa6bf882"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, e := freshKey()
	if e != nil {
		t.Fatal(e)
	}
	clientKey, e := freshKey()
	if e != nil {
		t.Fatal(e)
	}
	binding := workload.Binding{Project: "demo", Name: "fixture", Workload: "web-api", Bucket: "uploads", Capability: "object-storage", Access: "read-write"}
	base := Object{"identities": []any{Object{"name": "fixture-admin", "credentials": []any{Object{"accessKey": admin.Access, "secretKey": admin.Secret}}, "actions": []string{"Admin"}}}}
	conf, e := ExtendProvider(base, []workload.Binding{binding}, map[string]Key{binding.ID(): clientKey})
	if e != nil {
		t.Fatal(e)
	}
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "auth.json")
	if e = os.WriteFile(path, workload.JSON(conf), 0600); e != nil {
		t.Fatal(e)
	}
	docker := func(args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, "docker", append([]string{"--context", "orbstack"}, args...)...)
		return c.Output()
	}
	raw, e := docker("run", "--detach", "--rm", "--label", "atlas.fixture=s2-policy", "--publish", "127.0.0.1::8333", "--tmpfs", "/data:rw,mode=1777", "--mount", "type=bind,src="+path+",dst=/fixture-auth.json,readonly", image, "mini", "-dir=/data", "-ip=127.0.0.1", "-ip.bind=0.0.0.0", "-s3.config=/fixture-auth.json", "-s3.iam=false", "-webdav=false", "-admin.ui=false", "-bucket=uploads", "-s3.autoCreateBucket=false", "-s3.allowDeleteBucketNotEmpty=false", "-s3.port.iceberg=0", "-s3.port.lance=0", "-master.volumeSizeLimitMB=16", "-volume.max=4")
	if e != nil {
		t.Fatal("fixture container start failed", e)
	}
	id := strings.TrimSpace(string(raw))
	defer func() {
		c := exec.Command("docker", "--context", "orbstack", "rm", "--force", id)
		if e := c.Run(); e != nil {
			t.Error("fixture cleanup failed", e)
		}
	}()
	raw, e = docker("inspect", id)
	if e != nil {
		t.Fatal(e)
	}
	var info []Object
	if e = json.Unmarshal(raw, &info); e != nil || len(info) != 1 {
		t.Fatal("fixture inspect", e)
	}
	ports := array(at(info[0], "NetworkSettings", "Ports", "8333/tcp"))
	if len(ports) != 1 || at(mapping(ports[0]), "HostIp") != "127.0.0.1" {
		t.Fatal("non-loopback fixture")
	}
	address := "http://127.0.0.1:" + str(at(mapping(ports[0]), "HostPort"))
	httpClient := &http.Client{Timeout: 5 * time.Second}
	defer httpClient.CloseIdleConnections()
	c := &webslice.Client{Endpoint: address, Bucket: "uploads", Access: clientKey.Access, Secret: clientKey.Secret, HTTP: httpClient}
	adminClient := &webslice.Client{Endpoint: address, Bucket: "uploads", Access: admin.Access, Secret: admin.Secret, HTTP: httpClient}
	ready := false
	for attempt := 0; attempt < 40; attempt++ {
		_, code, e := adminClient.Request(ctx, "GET", "/uploads?list-type=2", nil)
		if e == nil && code == 200 {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if !ready {
		t.Fatal("fixture did not become ready")
	}
	if _, e = c.RoundTrip(ctx, []byte("fixture Web S3 roundtrip")); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/uploads?list-type=2", "/uploads?uploads="} {
		if _, code, e := c.Request(ctx, "GET", path, nil); e != nil || code != 200 {
			t.Fatalf("listing not granted %s: %d %v", path, code, e)
		}
	}
	object := "/uploads/fixture-tagged"
	if _, code, e := c.Request(ctx, "PUT", object, []byte("tagged")); e != nil || code != 200 {
		t.Fatal("tag object put", e, code)
	}
	tags := []byte(`<Tagging><TagSet><Tag><Key>purpose</Key><Value>fixture</Value></Tag></TagSet></Tagging>`)
	for _, q := range []struct {
		method, path string
		body         []byte
		status       int
	}{{"HEAD", object, nil, 200}, {"PUT", object + "?tagging=", tags, 200}, {"GET", object + "?tagging=", nil, 200}, {"DELETE", object + "?tagging=", nil, 204}, {"DELETE", object, nil, 204}} {
		if _, code, e := c.Request(ctx, q.method, q.path, q.body); e != nil || code != q.status {
			t.Fatalf("object/tagging %s %s: %d %v", q.method, q.path, code, e)
		}
	}
	raw, code, e := c.Request(ctx, "POST", "/uploads/fixture-multipart?uploads=", nil)
	if e != nil || code != 200 {
		t.Fatal("multipart initiate", e, code)
	}
	var upload struct {
		ID string `xml:"UploadId"`
	}
	if e = xml.Unmarshal(raw, &upload); e != nil || upload.ID == "" {
		t.Fatal("multipart id", e)
	}
	multipart := "/uploads/fixture-multipart?uploadId=" + url.QueryEscape(upload.ID)
	for _, q := range []struct {
		method, path string
		body         []byte
		status       int
	}{{"PUT", multipart + "&partNumber=1", []byte("part"), 200}, {"GET", multipart, nil, 200}, {"DELETE", multipart, nil, 204}} {
		if _, code, e := c.Request(ctx, q.method, q.path, q.body); e != nil || code != q.status {
			t.Fatalf("multipart %s: %d %v", q.method, code, e)
		}
	}
	if e = c.PermissionProbe(ctx); e != nil {
		t.Fatal(e)
	}
	for _, q := range []struct {
		method, path string
		body         []byte
	}{
		{"PUT", "/uploads?cors=", []byte(`<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>`)},
		{"PUT", "/atlas-s2-forbidden", nil},
		{"DELETE", "/uploads", nil},
		{"PUT", "/atlas-s2-denied/object", []byte("must be denied")},
	} {
		_, code, e := c.Request(ctx, q.method, q.path, q.body)
		if e != nil || code != 403 {
			t.Fatal(fmt.Sprintf("policy did not deny %s %s (status %d)", q.method, q.path, code), e)
		}
	}
	// The denied DeleteBucket targeted only this empty fixture. Prove it remains.
	if _, code, e := adminClient.Request(ctx, "GET", "/uploads?list-type=2", nil); e != nil || code != 200 {
		t.Fatal("fixture bucket changed")
	}
	second, e := ExtendProvider(conf, []workload.Binding{binding}, map[string]Key{binding.ID(): clientKey})
	if e != nil || !bytes.Equal(workload.JSON(second), workload.JSON(conf)) {
		t.Fatal("provider config not stable", e)
	}
	t.Log("SeaweedFS 4.47 policy-only principal: object PUT/GET/DELETE PASS; cross-bucket/CreateBucket/DeleteBucket/PutBucketCORS denied; isolated empty bucket retained")
}
