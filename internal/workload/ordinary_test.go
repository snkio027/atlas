package workload

import (
	"bytes"
	"github.com/snkio027/atlas/internal/platform"
	"strings"
	"testing"
)

func TestOrdinarySingleWebServiceCompilesWithoutAcceptancePeer(t *testing.T) {
	c, m := compileFixture(t)
	in := m.Intent
	in.Workloads = in.Workloads[:1]
	in.Workloads[0].Image = "atlas.local/experiment-archive:v1@sha256:" + strings.Repeat("a", 64)
	// Keep just the binding of this service, if any; ordinary no-binding also works.
	for _, bound := range []bool{true, false} {
		t.Run(map[bool]string{true: "s3", false: "no-binding"}[bound], func(t *testing.T) {
			input := in
			input.Bindings = []Binding{}
			if bound {
				input.Bindings = []Binding{{Schema: 1, Kind: "CapabilityBinding", Project: in.Project.Name, Name: "archive-storage", Workload: in.Workloads[0].Name, Capability: "object-storage", Bucket: "uploads", Access: "read-write"}}
			}
			model, err := Resolve(input, true)
			if err != nil {
				t.Fatal(err)
			}
			var a *Artifacts
			if bound {
				a = artifactFixture(c, model)
			}
			first, err := Compile(c, model, "consumer", a)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Compile(c, model, "consumer", a)
			if err != nil {
				t.Fatal(err)
			}
			if platform.BundleDigest(first.Files) != platform.BundleDigest(second.Files) {
				t.Fatal("non deterministic ordinary compile")
			}
			for name, raw := range first.Files {
				if strings.HasPrefix(name, "gitops/workloads/projects/") && (bytes.Contains(raw, []byte("roundtrip")) || bytes.Contains(raw, []byte("unbound"))) {
					t.Fatal("acceptance fixture leaked", name)
				}
			}
		})
	}
}
func TestAuthoredBudgetErrorsNameFileAndField(t *testing.T) {
	in := fixture(t)
	in.Workloads[0].Resources.Requests.Memory = 1000000
	_, err := Resolve(in, true)
	if err == nil || !strings.Contains(err.Error(), "platform/workloads/") || !strings.Contains(err.Error(), "resources") {
		t.Fatal(err)
	}
	in = fixture(t)
	in.Project.Quota.Pods = 1
	_, err = Resolve(in, true)
	if err == nil || !strings.Contains(err.Error(), "platform/projects/") || !strings.Contains(err.Error(), "quota") {
		t.Fatal(err)
	}
}
