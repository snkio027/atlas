package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func runObservation(args []string) int {
	f := flag.NewFlagSet("atlas-platform observe|verify", flag.ContinueOnError)
	root := f.String("root", ".", "reviewed source checkout")
	input := f.String("expectation", "", "explicit observation expectation JSON (target, Applications and resource assertions)")
	revision := f.String("revision", "", "exact expected Git commit SHA")
	kubeconfig := f.String("kubeconfig", "", "owner-only kubeconfig matching the expectation binding")
	kubectl := f.String("kubectl", "kubectl", "locked local kubectl; version/config view only")
	id := f.String("id", "", "unique evidence ID; cannot overwrite a previous observation")
	if e := f.Parse(args); e != nil {
		return 2
	}
	if f.NArg() != 0 || *input == "" || *kubeconfig == "" || *id == "" || !observation.FullSHA(*revision) {
		f.Usage()
		return 2
	}
	fail := func(e error) int { fmt.Fprintln(os.Stderr, e); return 2 }
	var expect observation.Expectation
	b, e := os.ReadFile(*input)
	if e != nil {
		return fail(e)
	}
	if e = observation.Decode(b, &expect, true); e != nil {
		return fail(e)
	}
	if e = expect.Validate(); e != nil {
		return fail(e)
	}
	if expect.Revision != *revision {
		return fail(fmt.Errorf("requested revision differs from expectation"))
	}
	impl, e := observation.ExecutableIdentity()
	if e != nil {
		return fail(e)
	}
	if impl.Dirty || impl.Revision != expect.ImplementationSHA {
		return fail(fmt.Errorf("observation requires the exact clean implementation build bound in the expectation"))
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return fail(e)
	}
	p, e := platform.Load(abs, platform.Tools{Helm: "helm", Kubectl: *kubectl, YQ: "yq"})
	if e != nil {
		return fail(e)
	}
	if e = p.VerifyArtifacts(); e != nil {
		return fail(e)
	}
	model, e := p.ObservationResourceModel()
	if e != nil {
		return fail(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	reader, e := observation.NewAPIReader(ctx, *kubeconfig, *kubectl, p.Lock.Kubectl, p.Lock.Kubernetes, expect.Target, model)
	var envelope observation.Envelope
	if e != nil {
		envelope = observation.Envelope{Schema: "atlas.observation/v1", Subject: expect.Subject, Target: expect.Target, ImplementationSHA: impl.Revision, ExpectedRevision: expect.Revision, ExpectationSHA256: observation.Digest(expect), StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), Classification: observation.Unknown, Reasons: []string{"READER_INITIALIZATION_UNAVAILABLE"}}
	} else {
		defer reader.Close()
		envelope, e = observation.Collect(ctx, reader, expect)
		if e != nil {
			return fail(e)
		}
	}
	envelope.BinarySHA256 = impl.BinarySHA256
	envelope.LockedToolVersions = map[string]string{"kubectl": p.Lock.Kubectl, "kubernetes": p.Lock.Kubernetes}
	path, e := observation.Save(filepath.Join(abs, ".state", "observations"), *id, envelope)
	if e != nil {
		return fail(e)
	}
	fmt.Print(envelope.Report())
	fmt.Println("Private evidence: " + path)
	return envelope.ExitCode()
}
