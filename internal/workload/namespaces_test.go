package workload

import (
	"strings"
	"testing"
)

func renameProject(in *Intent, name string) {
	in.Project.Name = name
	for i := range in.Workloads {
		in.Workloads[i].Project = name
	}
	for i := range in.Bindings {
		in.Bindings[i].Project = name
	}
}
func TestProjectCannotClaimUnprefixedPlatformNamespaces(t *testing.T) {
	for _, name := range []string{"gateway", "monitoring", "sealed-secrets", "object-storage", "cert-manager", "envoy-gateway-system", "local-path-storage"} {
		t.Run(name, func(t *testing.T) {
			in := fixture(t)
			renameProject(&in, name)
			if _, err := Resolve(in, true); err == nil || !strings.Contains(err.Error(), "reserved Project") {
				t.Fatalf("platform namespace accepted: %v", err)
			}
		})
	}
}
func TestFuturePlatformDestinationReservesNamespaceBeforeCreation(t *testing.T) {
	c, _ := compileFixture(t)
	in := fixture(t)
	renameProject(&in, "database-system")
	m := resolved(t, in)
	err := edit(c.Base, ProjectsPath, func(xs []Object) ([]Object, error) {
		for _, p := range xs {
			if meta(p)["name"] == "platform-project" {
				obj(p["spec"])["destinations"] = append(arr(val(p, "spec", "destinations")), Object{"namespace": "database-system", "server": "https://kubernetes.default.svc"})
			}
		}
		return xs, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(c, m, "permissions", nil); err == nil || !strings.Contains(err.Error(), "reserved by the deployment base") {
		t.Fatalf("future platform reservation missed: %v", err)
	}
}
