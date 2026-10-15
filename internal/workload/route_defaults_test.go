package workload

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The fixture contains only HTTPRoute specs read from the Gateway API v1.6.1
// server during the atlas-s2-r1 clean run. Both HTTP and HTTPS routes stayed
// OutOfSync after successful Argo syncs because CRD defaults changed their specs.
// Keeping generated specs equal to that API representation avoids weakening
// observation or adding ignoreDifferences for an actual compiler omission.
func TestConsumerRoutesMatchGatewayAPIDefaults(t *testing.T) {
	c, _ := compileFixture(t)
	c.HTTPSPort = 18443
	in := fixture(t)
	peer := in.Workloads[0]
	peer.Name = "unbound"
	peer.Exposure.Hostname = "unbound.atlas.test"
	in.Workloads = append(in.Workloads, peer)
	m := resolved(t, in)
	result, err := Compile(c, m, "consumer", artifactFixture(c, m))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/httproute-v1.6.1-defaulted-specs.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]Object
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	for _, spec := range expected {
		for _, raw := range arr(spec["parentRefs"]) {
			obj(raw)["namespace"] = "platform-gateway"
		}
	}
	seen := 0
	for path, raw := range result.Files {
		if !strings.HasPrefix(path, "gitops/workloads/projects/demo/") || !strings.HasSuffix(path, "/resources.json") {
			continue
		}
		values, err := objects(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range values {
			if route["kind"] != "HTTPRoute" {
				continue
			}
			name := str(meta(route)["name"])
			want, ok := expected[name]
			if !ok {
				t.Fatalf("unexpected route %s", name)
			}
			if meta(route)["namespace"] != "project-demo" || !bytes.Equal(JSON(route["spec"]), JSON(want)) {
				t.Errorf("%s: compiled route differs from the locked API defaulted spec", name)
			}
			delete(expected, name)
			seen++
		}
	}
	if seen != 4 || len(expected) != 0 {
		t.Fatalf("incomplete route coverage: %d routes, %d missing", seen, len(expected))
	}
}
