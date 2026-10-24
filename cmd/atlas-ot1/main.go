// atlas-ot1 prepares and verifies the exact OT-1 ceremony. This binary exposes
// a single bounded run command. It requires the exact reviewed plan digest and
// independent OT-1 target; preparation/capture remain read-only toward clusters.
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
	code, e := run(ctx, os.Args[1:])
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
	}
	os.Exit(code)
}
func run(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("usage: atlas-ot1 prepare-profile|plan|inspect-plan|capture|verify-attempt|prepare-action|run|plan-continuation|check-continuation|continue [flags]")
	}
	verb := args[0]
	switch verb {
	case "prepare-profile", "plan", "inspect-plan", "capture", "verify-attempt", "prepare-action", "run", "plan-continuation", "check-continuation", "continue":
	default:
		return 2, errors.New("unknown OT-1 command")
	}
	f := flag.NewFlagSet("atlas-ot1 "+verb, flag.ContinueOnError)
	bundle := f.String("predecessor-bundle", "", "immutable SOURCE_RELEASED authority bundle")
	manifest := f.String("predecessor-manifest", "", "reviewed authority manifest SHA256")
	root := f.String("root", ".", "implementation checkout")
	runtimeRepo := f.String("runtime-repo", "", "private checkout that created the bound OT-1 cluster")
	toolDir := f.String("tool-dir", "", "locked tool directory for bounded run")
	approval := f.String("approve-plan", "", "explicit approval of the complete canonical plan SHA256; run only")
	repo := f.String("repo", "", "private baseline/plan checkout")
	out := f.String("output", "", "new private output path")
	clone := f.String("output-repo", "", "new private clone for the seven desired revisions")
	targetFile := f.String("target-binding", "", "explicit target binding JSON, including cluster UID and kubeconfig SHA256")
	planFile := f.String("plan", "", "canonical private plan JSON")
	stage := f.String("stage", "", "exact compiled stage name")
	kubeconfig := f.String("kubeconfig", "", "explicit private kubeconfig")
	baselineFile := f.String("baseline-snapshot", "", "private BASELINE_ADOPTED snapshot")
	input := f.String("input", "", "input attempt directory or freshly read Application JSON")
	action := f.String("action", "", "create, mode, sync or release (preparation only)")
	uid := f.String("uid", "", "previously verified exact Application UID")
	owner := f.String("owner", "", "one of the four compiled foundation owners")
	absent := f.Bool("confirmed-absent", false, "only for create preparation; requires a fresh successful inventory read")
	helm := f.String("helm", "helm", "locked local Helm")
	kubectl := f.String("kubectl", "kubectl", "locked local kubectl")
	yq := f.String("yq", "yq", "locked local yq")
	if e := f.Parse(args[1:]); e != nil {
		return 2, e
	}
	if f.NArg() != 0 {
		return 2, errors.New("unexpected positional arguments")
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return 2, e
	}
	tools := platform.Tools{Helm: *helm, Kubectl: *kubectl, YQ: *yq}
	parent := ctx
	started := time.Now()
	// Bound preparation while the plan is still untrusted. A validated run gets
	// its full derived budget below, without inheriting this shorter deadline.
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	switch verb {
	case "plan-continuation":
		if *bundle == "" || *manifest == "" || *repo == "" || *out == "" {
			return 2, errors.New("plan-continuation requires predecessor bundle/manifest, repo and output")
		}
		old, e := ot1.LoadPredecessor(ctx, abs, *repo, *bundle, *manifest)
		if e != nil {
			return 2, e
		}
		impl, e := observation.ExecutableIdentity()
		if e != nil {
			return 2, e
		}
		plan, e := ot1.PrepareContinuation(old, impl)
		if e != nil {
			return 2, e
		}
		if e = ot1.VerifyRepositoryPlan(ctx, *repo, plan); e != nil {
			return 2, e
		}
		if e = ot1.SavePlan(*out, plan); e != nil {
			return 2, e
		}
		fmt.Printf("Prepared SOURCE_RELEASED continuation SHA256: %s\nRuntime: NOT_RUN\n", observation.Digest(plan))
		return 0, nil
	case "prepare-profile":
		if *out == "" {
			return 2, errors.New("--output is required")
		}
		result, e := ot1.PrepareRepository(ctx, abs, *out, tools)
		if e != nil {
			return 2, e
		}
		fmt.Print(string(observation.Bytes(result)))
		return 0, nil
	case "plan":
		if *repo == "" || *clone == "" || *targetFile == "" || *out == "" {
			return 2, errors.New("plan requires --repo, --output-repo, --target-binding and --output")
		}
		var target observation.Target
		b, e := observation.RegularPrivate(*targetFile)
		if e != nil {
			return 2, e
		}
		if e = observation.Decode(b, &target, true); e != nil {
			return 2, e
		}
		impl, e := observation.ExecutableIdentity()
		if e != nil {
			return 2, e
		}
		p, e := ot1.CompilePlan(ctx, abs, *repo, *clone, target, impl, tools)
		if e != nil {
			return 2, e
		}
		if e = ot1.SavePlan(*out, p); e != nil {
			return 2, e
		}
		fmt.Printf("Prepared plan SHA256: %s\nRuntime: NOT_RUN\n", observation.Digest(p))
		return 0, nil
	}
	if *planFile == "" || *repo == "" {
		return 2, errors.New("--plan and --repo are required")
	}
	plan, e := ot1.ReadPlan(ctx, abs, *repo, *planFile)
	if e != nil {
		return 2, e
	}
	if verb == "run" || verb == "continue" {
		cancel()
		ctx, cancel = runContext(parent, started, plan)
		defer cancel()
	}
	if verb == "inspect-plan" {
		fmt.Print(string(observation.Bytes(observation.Object{"planSHA256": observation.Digest(plan), "target": plan.Target, "steps": ot1.Steps(plan), "runtime": "NOT_RUN"})))
		return 0, nil
	}
	desired, e := ot1.DesiredObjects(ctx, *repo, plan)
	if e != nil {
		return 2, e
	}
	if verb == "run" || verb == "continue" || verb == "check-continuation" {
		if (verb != "check-continuation" && *approval != observation.Digest(plan)) || *out == "" || *runtimeRepo == "" || *toolDir == "" {
			return 2, errors.New("run requires exact --approve-plan, --output, --runtime-repo and --tool-dir")
		}
		impl, e := observation.ExecutableIdentity()
		if e != nil {
			return 2, e
		}
		if impl != plan.Implementation {
			return 2, errors.New("run requires the exact clean binary bound in the plan")
		}
		runtimeAbs, e := filepath.Abs(*runtimeRepo)
		if e != nil {
			return 2, e
		}
		repoAbs, e := filepath.Abs(*repo)
		if e != nil {
			return 2, e
		}
		lockedTools := platform.Tools{Helm: filepath.Join(*toolDir, "helm"), Kubectl: filepath.Join(*toolDir, "kubectl"), YQ: filepath.Join(*toolDir, "yq")}
		p, e := platform.Load(runtimeAbs, lockedTools)
		if e != nil {
			return 2, e
		}
		if e = p.VerifyArtifacts(); e != nil {
			return 2, e
		}
		model, e := p.ObservationResourceModel()
		if e != nil {
			return 2, e
		}
		reader, e := observation.NewAPIReader(ctx, filepath.Join(runtimeAbs, ".state/kubeconfig"), lockedTools.Kubectl, p.Lock.Kubectl, p.Lock.Kubernetes, plan.Target, model)
		if e != nil {
			return 2, e
		}
		defer reader.Close()
		executor := &ot1.Executor{Plan: plan, Repository: repoAbs, RuntimeRepository: runtimeAbs, ToolDirectory: *toolDir, EvidenceDirectory: *out, Reader: reader, Runner: atlas.ExecRunner{Root: runtimeAbs, ToolDir: *toolDir, DockerContext: "orbstack"}, Desired: desired}
		if verb != "run" {
			if *bundle == "" || *manifest == "" {
				return 2, errors.New("continuation requires immutable predecessor bundle and manifest")
			}
			old, e := ot1.LoadPredecessor(ctx, abs, repoAbs, *bundle, *manifest)
			if e != nil {
				return 2, e
			}
			if e = old.CheckPlan(plan); e != nil {
				return 2, e
			}
			if verb == "continue" {
				e = executor.Continue(ctx, *approval, old)
				return boolCode(e), e
			}
			snapshot, e := executor.ContinuationAnchor(ctx, old)
			if e != nil {
				if save := observation.CreatePrivate(filepath.Join(*out, "failed-anchor.json"), observation.Bytes(snapshot)); save != nil {
					return 2, save
				}
				return 2, e
			}
			if e = ot1.SaveContinuationCheck(*out, plan, snapshot); e != nil {
				return 2, e
			}
			fmt.Printf("CONTINUATION_ANCHOR_SOURCE_RELEASED VERIFIED; plan=%s; live mutation=0; STOP lock unchanged\n", observation.Digest(plan))
			return 0, nil
		}
		if e = executor.Preflight(ctx); e != nil {
			return 2, e
		}
		lockPath := filepath.Join(runtimeAbs, ".state/ot1-run.lock")
		if e = observation.CreatePrivate(lockPath, observation.Bytes(observation.Object{"planSHA256": *approval, "attempt": *out, "createdAt": time.Now().UTC()})); e != nil {
			return 2, errors.New("OT-1 run lock exists or cannot be created; inspect the prior attempt before proceeding")
		}
		e = ot1.Run(ctx, plan, *approval, *out, desired, executor)
		if e == nil {
			e = os.Remove(lockPath)
		} // failure/interruption deliberately retains the lock
		return boolCode(e), e
	}

	if verb == "verify-attempt" {
		if *input == "" || *out == "" {
			return 2, errors.New("--input and --output are required")
		}
		return ot1.Replay(plan, *input, *out, desired)
	}
	index := -1
	for i, p := range plan.Phases {
		if p.Stage.Name == *stage {
			index = i
		}
	}
	if index < 0 {
		return 2, errors.New("--stage must identify a compiled phase")
	}
	phase := plan.Phases[index]
	if *out == "" {
		return 2, errors.New("--output is required (create-only)")
	}
	if verb == "capture" {
		impl, e := observation.ExecutableIdentity()
		if e != nil {
			return 2, e
		}
		if impl != plan.Implementation {
			return 2, errors.New("capture requires the exact clean binary bound in the plan")
		}
		if *kubeconfig == "" {
			return 2, errors.New("--kubeconfig is required")
		}
		p, e := platform.Load(*repo, tools)
		if e != nil {
			return 2, e
		}
		if e = p.VerifyArtifacts(); e != nil {
			return 2, e
		}
		model, e := p.ObservationResourceModel()
		if e != nil {
			return 2, e
		}
		reader, e := observation.NewAPIReader(ctx, *kubeconfig, *kubectl, p.Lock.Kubectl, p.Lock.Kubernetes, plan.Target, model)
		if e != nil {
			now := time.Now().UTC()
			failed := ot1.Snapshot{Schema: 1, PlanSHA256: observation.Digest(plan), Stage: phase.Stage.Name, Revision: phase.Revision, InventoryError: "READER_INITIALIZATION_UNAVAILABLE", Envelope: observation.Envelope{Schema: "atlas.observation/v1", Subject: "ot1/" + phase.Stage.Name, Target: plan.Target, ImplementationSHA: impl.Revision, BinarySHA256: impl.BinarySHA256, ExpectedRevision: phase.Revision, StartedAt: now, FinishedAt: now, Classification: observation.Unknown, Reasons: []string{"READER_INITIALIZATION_UNAVAILABLE"}}}
			if saveErr := observation.CreatePrivate(*out, observation.Bytes(failed)); saveErr != nil {
				return 2, saveErr
			}
			return 2, e
		}
		defer reader.Close()
		var baseline *ot1.Snapshot
		if index > 0 {
			if *baselineFile == "" {
				return 2, errors.New("post-baseline capture requires --baseline-snapshot")
			}
			s, e := ot1.ReadSnapshot(*baselineFile)
			if e != nil {
				return 2, e
			}
			if s.PlanSHA256 != observation.Digest(plan) || s.Stage != plan.Phases[0].Stage.Name {
				return 2, errors.New("wrong baseline snapshot")
			}
			baseline = &s
		}
		s, e := ot1.Capture(ctx, reader, plan, index, baseline)
		if e != nil {
			return 2, e
		}
		if e = observation.CreatePrivate(*out, observation.Bytes(s)); e != nil {
			return 2, e
		}
		fmt.Printf("Captured %s; private evidence %s\nUse verify-attempt for stage interpretation; capture is not a Gate PASS.\n", phase.Stage.Name, *out)
		if s.InventoryError != "" {
			return 2, errors.New(s.InventoryError)
		}
		return s.Envelope.ExitCode(), nil
	}
	var current observation.Object
	if *action != "create" {
		b, e := observation.RegularPrivate(*input)
		if e != nil {
			return 2, e
		}
		if e = observation.Decode(b, &current, false); e != nil {
			return 2, e
		}
	}
	var payload any
	method := "PATCH"
	url := ""
	switch *action {
	case "create":
		payload, e = ot1.CreateApplication(phase, *owner, *absent)
		method = "POST"
		url = "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications"
	case "mode":
		if index == 0 {
			return 2, errors.New("baseline has no mode transition")
		}
		payload, e = ot1.ModePatch(current, *uid, plan.Phases[index-1], phase)
	case "sync":
		payload, e = ot1.SyncPatch(current, *uid, phase)
	case "release":
		if index == 0 || phase.Stage.Outcome != "released" {
			return 2, errors.New("not a release stage")
		}
		name := observation.Reference(current).Name
		var expected observation.Object
		for _, a := range plan.Phases[index-1].Applications {
			if a.Name == name {
				expected = observation.Object{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": observation.Object{"name": name, "namespace": "argocd"}, "spec": a.Spec}
			}
		}
		permitted := false
		for _, n := range ot1.Steps(plan)[index].Release {
			permitted = permitted || n == name
		}
		if !permitted {
			return 2, errors.New("Application outside this release stage")
		}
		url, payload, e = ot1.DeleteRequest(current, expected, *uid, phase.Revision)
		method = "DELETE"
	default:
		return 2, errors.New("--action must be create|mode|sync|release")
	}
	if e != nil {
		return 2, e
	}
	if url == "" {
		url = "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/" + observation.Reference(current).Name
	}
	e = observation.CreatePrivate(*out, observation.Bytes(observation.Object{"planSHA256": observation.Digest(plan), "target": plan.Target, "stage": phase.Stage.Name, "revision": phase.Revision, "method": method, "url": url, "payload": payload, "execution": "NOT_EXECUTED; fresh preconditions and separate bounded-plan approval required"}))
	return boolCode(e), e
}
func boolCode(e error) int {
	if e != nil {
		return 2
	}
	return 0
}

// runContext requires a validated plan. Include preparation time in the fixed
// overhead; respect caller cancellation without retaining the preparation timer.
func runContext(parent context.Context, started time.Time, plan ot1.Plan) (context.Context, context.CancelFunc) {
	budget := time.Duration(len(plan.Phases))*time.Duration(plan.MaxStageSeconds)*time.Second + 15*time.Minute
	return context.WithDeadline(parent, started.Add(budget))
}
