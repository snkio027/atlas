package main

import (
	"atlas-refactor/internal/managementaccess"
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/workload"
	"errors"
	"flag"
	"fmt"
	"io"
)

func runManagementAccess(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("management-access", flag.ContinueOnError)
	root := fs.String("root", ".", "compiler source checkout with locked CRD schemas")
	input := fs.String("deployment-dir", "", "local export of the exact deployed Git commit")
	output := fs.String("output", "", "new local review directory; never a deployment checkout")
	port := fs.Int("https-port", 0, "existing installation loopback HTTPS port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *input == "" || *output == "" {
		return errors.New("management-access requires --deployment-dir, --https-port and --output (local rendering only)")
	}
	p, err := platform.Load(*root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if err != nil {
		return err
	}
	if err = p.VerifyArtifacts(); err != nil {
		return err
	}
	model, err := p.ObservationResourceModel()
	if err != nil {
		return err
	}
	base, err := managementaccess.ReadTree(*input)
	if err != nil {
		return err
	}
	result, err := managementaccess.Render(base, model, *port)
	if err != nil {
		return err
	}
	files := managementaccess.Files{}
	for k, v := range result.Prerequisites {
		files["01-prerequisites/"+k] = v
	}
	for k, v := range result.Routes {
		files["02-routes/"+k] = v
	}
	report := map[string]any{"baseGitOpsSHA256": platform.BundleDigest(base), "httpsPort": *port, "endpoints": managementaccess.Endpoints(), "prerequisiteFilesSHA256": platform.BundleDigest(result.Prerequisites), "routeFilesSHA256": platform.BundleDigest(result.Routes), "clusterOperations": 0, "publication": "not performed", "precondition": "completed D1 and reviewed exact Git parent; certificate Ready Gate before routes", "compatibility": "historical app plans remain immutable; they reject a subsequent platform maintenance commit"}
	files["summary.json"] = workload.JSON(report)
	if err = managementaccess.WriteNew(*output, files); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Prepared two local GitOps deltas. Review before publication; no cluster, Git, credential, DNS or host-trust changes.")
	return err
}
