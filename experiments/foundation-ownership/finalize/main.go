//go:build ot1_finalization

// Fixed stage-23 continuation executable. No selectable stage, cluster or predecessor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"github.com/snkio027/atlas/internal/observation"
	"github.com/snkio027/atlas/internal/ot1"
	"github.com/snkio027/atlas/internal/platform"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(2)
	}
}
func run(parent context.Context) error {
	root := flag.String("root", ".", "implementation checkout")
	output := flag.String("output", "", "private new plan/anchor/attempt path")
	mode := flag.String("mode", "check", "plan, check, or execute this fixed continuation")
	planPath := flag.String("plan", "", "reviewed canonical plan")
	approval := flag.String("approve-plan", "", "exact plan SHA256 for execution")
	flag.Parse()
	if flag.NArg() != 0 || *output == "" {
		return errors.New("output required; no positional arguments")
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	repo := filepath.Join(abs, ".state/latest/ot1/desired")
	runtime := filepath.Join(abs, ".state/latest/ot1/source/.state/development/atlas-refactor-test-ot1/repo")
	tools := filepath.Join(abs, ".state/tools")
	budget := 10 * time.Minute
	if *mode == "execute" {
		budget = 40 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	f, e := ot1.LoadFinalization(ctx, abs, repo, filepath.Join(abs, ".state/authority/ot1-b195dc63"), filepath.Join(abs, ".state/authority/ot1-stage12-619e492b"))
	if e != nil {
		return e
	}
	impl, e := observation.ExecutableIdentity()
	if e != nil {
		return e
	}
	expected, e := f.Plan(impl)
	if e != nil {
		return e
	}
	if *mode == "plan" {
		if e = ot1.SavePlan(*output, expected); e != nil {
			return e
		}
		fmt.Printf("Finalization plan SHA256: %s\nFirst index: 24\nRuntime: NOT_RUN\n", observation.Digest(expected))
		return nil
	}
	if *mode != "check" && *mode != "execute" {
		return errors.New("unsupported one-off mode")
	}
	p, e := ot1.ReadPlan(ctx, abs, repo, *planPath)
	if e != nil {
		return e
	}
	if observation.Digest(p) != observation.Digest(expected) {
		return errors.New("Finalization requires exact clean executable and unchanged original plan")
	}
	locked := platform.Tools{Helm: filepath.Join(tools, "helm"), Kubectl: filepath.Join(tools, "kubectl"), YQ: filepath.Join(tools, "yq")}
	platformModel, e := platform.Load(runtime, locked)
	if e != nil {
		return e
	}
	if e = platformModel.VerifyArtifacts(); e != nil {
		return e
	}
	model, e := platformModel.ObservationResourceModel()
	if e != nil {
		return e
	}
	reader, e := observation.NewAPIReader(ctx, filepath.Join(runtime, ".state/kubeconfig"), locked.Kubectl, platformModel.Lock.Kubectl, platformModel.Lock.Kubernetes, p.Target, model)
	if e != nil {
		return e
	}
	defer reader.Close()
	desired, e := ot1.DesiredObjects(ctx, repo, p)
	if e != nil {
		return e
	}
	x := &ot1.Executor{Plan: p, Repository: repo, RuntimeRepository: runtime, ToolDirectory: tools, EvidenceDirectory: *output, Reader: reader, Runner: atlas.ExecRunner{Root: runtime, ToolDir: tools, DockerContext: "orbstack"}, Desired: desired}
	if *mode == "execute" {
		return x.ExecuteFinalization(ctx, *approval, f)
	}
	_, e = x.FinalizationAnchor(ctx, f, *output)
	if e != nil {
		return e
	}
	fmt.Println("Finalization read-only Ownership/Gate-B anchor VERIFIED; next original index 24; STOP lock unchanged")
	return nil
}
