package ot1

import (
	"context"
	"errors"
	"github.com/snkio027/atlas/internal/atlas"
	"strings"
)

// ReadOnlyRunner is used when collecting a repeated normal apply proof. It
// refuses a write before execution, even if the existing Bootstrap engine were
// to regress and try to reacquire authority. It never substitutes fake success.
type ReadOnlyRunner struct {
	Delegate atlas.Runner
	Requests []atlas.Request
	Denied   int
}

func (r *ReadOnlyRunner) Run(ctx context.Context, q atlas.Request) ([]byte, error) {
	r.Requests = append(r.Requests, q)
	if !ReadOnlyRequest(q) {
		r.Denied++
		return nil, errors.New("repeat-apply proof denied a non-read-only request")
	}
	return r.Delegate.Run(ctx, q)
}
func ReadOnlyRequest(q atlas.Request) bool {
	if q.InputPath != "" || len(q.Args) == 0 {
		return false
	}
	a := q.Args
	if len(q.Input) > 0 {
		// The existing engine decodes rendered manifests with strictly client-side
		// dry run; this performs discovery GETs but never sends a create request.
		if q.Tool != "kubectl" || len(a) != 12 || a[0] != "--kubeconfig" || a[2] != "--context" || a[4] != "--request-timeout=30s" {
			return false
		}
		switch strings.Join(a[5:], " ") {
		case "create --dry-run=client --validate=false -f - -o json",
			"get -f - --ignore-not-found=true --show-managed-fields -o json":
			// The second form reads the rendered Seed identities from stdin;
			// passing a manifest to kubectl get does not grant write authority.
			return true
		default:
			return false
		}
	}
	switch q.Tool {
	case "git":
		return a[0] == "status" || a[0] == "rev-parse" || a[0] == "ls-remote"
	case "helm":
		return a[0] == "version" || a[0] == "template"
	case "kind":
		return a[0] == "version" || len(a) > 1 && a[0] == "get" && (a[1] == "clusters" || a[1] == "nodes")
	case "docker":
		return len(a) > 1 && (a[0] == "context" && a[1] == "inspect" || a[0] == "image" && a[1] == "inspect" || a[0] == "inspect")
	case "kubectl":
		if a[0] == "version" {
			return true
		}
		// Only the engine's explicit kubeconfig/context prefix and read verb.
		if len(a) >= 6 && a[0] == "--kubeconfig" && a[2] == "--context" && strings.HasPrefix(a[4], "--request-timeout=") {
			return a[5] == "get" || strings.Join(a[5:], " ") == "create --dry-run=client --validate=false -f - -o json"
		}
	}
	return false
}
