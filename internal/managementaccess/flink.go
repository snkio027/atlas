package managementaccess

import (
	"errors"
	"github.com/snkio027/atlas/internal/platform"
	"github.com/snkio027/atlas/internal/platformnames"
)

const FlinkPath = "gitops/platform/data/flink/overlays/development/resources.json"

func flinkEndpoint() Endpoint {
	return Endpoint{"flink", platformnames.Flink, "flink-session-rest", 8081, 8081, map[string]string{"app": "flink-session", "component": "jobmanager", "type": "flink-standalone-kubernetes"}, true}
}

func validateFlink(base Files) error {
	xs, err := platform.DecodeJSONManifests(base[FlinkPath])
	if err != nil {
		return err
	}
	cr, err := get(xs, "FlinkDeployment", platformnames.Flink, "flink-session")
	if err != nil {
		return err
	}
	if at(cr, "spec", "mode") != "standalone" || at(cr, "spec", "flinkVersion") != "v2_3" || at(cr, "spec", "flinkConfiguration", "kubernetes.rest-service.exposed.type") != "ClusterIP" {
		return errors.New("Flink overview requires the reviewed standalone v2_3 ClusterIP contract")
	}
	if value := at(cr, "spec", "flinkConfiguration", "rest.port"); value != nil && value != "8081" {
		return errors.New("Flink REST port differs")
	}
	return nil
}

// A finite allowlist avoids treating all GET endpoints as harmless. In particular
// /jobs/:id/yarn-cancel and cancel-with-savepoint are never routed. This first
// surface supports the overview and static UI assets; job actions use RBAC CLI.
func flinkReadMatches() []any {
	out := []any{}
	for _, path := range []string{"/", "/index.html", "/config", "/overview", "/jobs/overview", "/taskmanagers"} {
		for _, method := range []string{"GET", "HEAD"} {
			out = append(out, Object{"path": Object{"type": "Exact", "value": path}, "method": method})
		}
	}
	// Top-level hashed Angular assets and the static assets directory only.
	for _, path := range []Object{{"type": "RegularExpression", "value": `^/[A-Za-z0-9_.-]+\.(js|css|woff2?|ttf|svg|png|ico)$`}, {"type": "PathPrefix", "value": "/assets/"}} {
		for _, method := range []string{"GET", "HEAD"} {
			out = append(out, Object{"path": path, "method": method})
		}
	}
	return out
}
