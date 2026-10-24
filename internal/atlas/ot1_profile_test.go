package atlas

import (
	"github.com/snkio027/atlas/internal/developmentprofile"
	"testing"
)

func TestOT1SourceCannotBypassProfileThroughLegacySchema(t *testing.T) {
	for _, schema := range []int{1, 2} {
		c := Config{Schema: schema, Cluster: "atlas-refactor-test-other", RepositoryURL: developmentprofile.Repository, Revision: developmentprofile.OT1Revision, GitOpsPath: "gitops/test", DockerContext: "orbstack", TimeoutSeconds: 300}
		if c.Validate() == nil {
			t.Fatal("OT-1 source admitted outside the exact schema-3 binding")
		}
	}
}
