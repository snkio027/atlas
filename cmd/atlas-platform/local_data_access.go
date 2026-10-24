package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/snkio027/atlas/internal/managementaccess"
	"github.com/snkio027/atlas/internal/platform"
	"github.com/snkio027/atlas/internal/workload"
)

func runLocalDataAccess(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("local-data-access", flag.ContinueOnError)
	root := fs.String("root", ".", "compiler checkout with locked CRD schemas")
	input := fs.String("deployment-dir", "", "local export of the exact deployed Git commit")
	output := fs.String("output", "", "new review directory")
	host := fs.String("node-host", "", "reviewed OrbStack data-worker hostname")
	client := fs.String("client-address", "", "measured Local NodePort client IPv4 address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *input == "" || *output == "" {
		return errors.New("local-data-access requires --deployment-dir, --node-host, --client-address and --output; local rendering only")
	}
	config := managementaccess.LocalDataConfig{NodeHost: *host, ClientAddress: *client}
	if err := config.Validate(); err != nil {
		return err
	}
	p, err := platform.Load(*root, platform.Tools{Helm: "helm", Kubectl: "kubectl", YQ: "yq"})
	if err != nil {
		return err
	}
	if err = p.VerifyArtifacts(); err != nil {
		return err
	}
	// Load locked schemas for optional installed data services without activating them.
	p.Capabilities.Active, err = p.ResolveCapabilities(append(p.Capabilities.Active, "postgresql", "redis", "kafka", "flink", "data-observability"))
	if err != nil {
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
	r, err := managementaccess.RenderLocalData(base, model, config)
	if err != nil {
		return err
	}
	files := managementaccess.Files{}
	for k, v := range r.Certificates {
		files["01-certificates/"+k] = v
	}
	for k, v := range r.Services {
		files["02-services/"+k] = v
	}
	files["summary.json"] = workload.JSON(map[string]any{"baseGitOpsSHA256": platform.BundleDigest(base), "config": config, "certificatesSHA256": platform.BundleDigest(r.Certificates), "servicesSHA256": platform.BundleDigest(r.Services), "clusterOperations": 0, "preconditions": []string{"exact Git parent and cluster UID", "one Ready data worker with all three backends local", "Kafka single nodeIds=[0]", "ports unused across live and desired services", "measured client source survives externalTrafficPolicy=Local", "new SAN certificates verified before services", "Redis and Kafka controlled rollout; server leaf certificates reissued; account credentials and CAs unchanged"}})
	if err = managementaccess.WriteNew(*output, files); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Prepared certificate and native TLS service deltas. Review the exact target and network preflight before publication; no cluster or credential operations.")
	return err
}
