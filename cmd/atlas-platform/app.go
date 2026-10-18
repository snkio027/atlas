package main

import (
	"atlas-refactor/internal/appworkspace"
	"atlas-refactor/internal/workload"
	"atlas-refactor/internal/workloadrun"
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const appHelp = `Usage: atlas-platform app <command> [flags]
  image    Package a static linux/arm64 Go executable (offline)
  init     Bind a new workspace to a completed D1 instance; generate intent
  check    Validate authored intent and registered image (offline)
  plan     Show deployment impact and save an exact plan (read-only remotely)
  deploy   Review and confirm the saved plan, then publish and wait
  status   Observe the recorded deployment, without phase/SHA arguments
  open     Foreground loopback access with verified upstream TLS
  logs     Last 100 workload log lines (explicit log output may contain app data)
  select-image  Update authored image references from a verified artifact

Use --workspace <directory> (default .); app.json binds the exact instance.
S2 platform acceptance remains under the advanced "workload" command.
No recovery, new-cluster creation, arbitrary manifests, or unqualified --yes.
`

func runApp(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Print(appHelp)
		return nil
	}
	verb := args[0]
	allowed := map[string][]string{
		"image": {"binary", "tag", "out", "json"},
		"init":  {"workspace", "instance", "product-source", "artifact", "project", "name", "owner", "s3"},
		"check": {"workspace", "json"}, "plan": {"workspace", "json"},
		"deploy": {"workspace", "plan", "approve-plan", "json"},
		"status": {"workspace", "json"}, "open": {"workspace", "service", "port"},
		"logs": {"workspace", "service"}, "select-image": {"workspace", "artifact"},
	}
	names, ok := allowed[verb]
	if !ok {
		return errors.New(appHelp)
	}
	f := flag.NewFlagSet("app "+verb, flag.ContinueOnError)
	workspace := f.String("workspace", ".", "managed app workspace")
	instance := f.String("instance", "", "completed D1 directory (installation.yaml; runtime.json at root or package/)")
	product := f.String("product-source", "", "matching Atlas product source (P0 source-build requirement)")
	artifact := f.String("artifact", "", "verified image.json from app image")
	project := f.String("project", "archive", "Project name")
	name := f.String("name", "experiment-archive", "WebService name")
	owner := f.String("owner", "", "Project owner")
	s3 := f.Bool("s3", false, "grant object-storage/uploads and generate one independent Binding")
	binary := f.String("binary", "", "static linux/arm64 binary")
	tag := f.String("tag", "", "explicit registry/repository:version")
	out := f.String("out", "", "new artifact output directory")
	jsonOutput := f.Bool("json", false, "one JSON result on stdout; progress/errors on stderr")
	approval := f.String("approve-plan", "", "automation: exact reviewed plan SHA256 (requires --plan)")
	planFile := f.String("plan", "", "automation: exact saved plan file")
	service := f.String("service", "", "Workload name from app status")
	port := f.Int("port", 0, "explicit loopback port; 0 allocates and reports one")
	f.Usage = func() {
		fmt.Fprint(f.Output(), appHelp)
		fmt.Fprintln(f.Output(), "Flags for", verb+":")
		for _, n := range names {
			v := f.Lookup(n)
			fmt.Fprintf(f.Output(), "  --%s: %s\n", n, v.Usage)
		}
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional argument; use --service for a workload")
	}
	var flagErr error
	f.Visit(func(v *flag.Flag) {
		if !contains(names, v.Name) {
			flagErr = fmt.Errorf("--%s is not valid for app %s", v.Name, verb)
		}
	})
	if flagErr != nil {
		return flagErr
	}
	root, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	switch verb {
	case "image":
		if *binary == "" || *tag == "" || *out == "" {
			return errors.New("--binary, --tag and --out required")
		}
		a, err := appworkspace.Package(*binary, *tag, *out)
		if err != nil {
			return err
		}
		if *jsonOutput {
			fmt.Print(string(workload.JSON(a)))
		} else {
			fmt.Println("Image:", a.Reference)
			fmt.Println("Register:", filepath.Join(*out, "image.json"))
		}
		return nil
	case "init":
		if *instance == "" || *product == "" || *artifact == "" || *owner == "" {
			return errors.New("--instance, --product-source, --artifact and --owner required")
		}
		err = appworkspace.Init(appworkspace.InitOptions{Root: root, Instance: *instance, ProductSource: *product, Artifact: *artifact, Project: *project, Name: *name, Owner: *owner, S3: *s3})
		if err == nil {
			fmt.Println("Workspace:", root)
			fmt.Println("Edit platform/ JSON; then app check, app plan, app deploy. No deployment performed.")
		}
		return err
	case "select-image":
		if *artifact == "" {
			return errors.New("--artifact required")
		}
		return appworkspace.SelectArtifact(root, *artifact)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if verb != "open" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Minute)
		defer cancel()
	}
	var w *workloadrun.Workflow
	var config string
	recorded := verb == "status" || verb == "open" || verb == "logs"
	if recorded {
		w, err = appworkspace.Recorded(root)
	} else {
		w, config, err = appworkspace.Load(root)
	}
	if err != nil {
		return err
	}
	w.Progress = func(s string) { fmt.Fprintln(os.Stderr, s) }
	var unlock func()
	if recorded {
		unlock, err = w.Install.ReadLock()
	} else {
		unlock, err = w.Lock()
	}
	if err != nil {
		return err
	}
	defer unlock()
	if verb == "check" {
		if *jsonOutput {
			fmt.Print(string(workload.JSON(map[string]any{"result": "VALID", "target": w.Install.Config.Cluster, "intent": w.Model.Intent})))
		} else {
			fmt.Printf("VALID: %s / %s; verified linux/arm64 artifact. No publication.\n", w.Install.Config.Cluster, w.Model.Intent.Project.Name)
		}
		return nil
	}
	if verb == "plan" {
		p, v, err := appPlan(ctx, w, root, config)
		if err != nil {
			return err
		}
		return printAppPlan(p, v, *jsonOutput)
	}
	if verb == "deploy" {
		if (*approval == "") != (*planFile == "") {
			return errors.New("automation requires both --plan and --approve-plan")
		}
		p, err := w.ReadPlan()
		if os.IsNotExist(err) && *approval == "" {
			p, _, err = appPlan(ctx, w, root, config)
		} else if err == nil {
			err = w.LoadBase(ctx)
		}
		if err != nil {
			return err
		}
		if p.ConfigSHA256 != workload.Digest(workload.JSON(w.Config)) || p.IntentSHA256 != workload.Digest(workload.JSON(w.Model.Intent)) {
			return errors.New("authored inputs or artifact changed; run app plan and review the new impact")
		}
		if *approval != "" {
			b, err := appworkspace.Read(*planFile, true)
			if err != nil {
				return err
			}
			if !bytes.Equal(b, workload.JSON(p)) || *approval != workload.Digest(b) {
				return errors.New("automation plan differs from workspace's exact plan")
			}
		} else {
			if *jsonOutput {
				return errors.New("--json deploy requires explicit --plan and --approve-plan; interactive review is human-readable")
			}
			v, err := w.Preview(ctx, p)
			if err != nil {
				return err
			}
			if err = printAppPlan(p, v, false); err != nil {
				return err
			}
			st, err := os.Stdin.Stat()
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeCharDevice == 0 {
				return errors.New("noninteractive deploy requires --plan and --approve-plan")
			}
			phrase := "deploy " + p.Cluster + "/" + p.Project
			fmt.Fprintf(os.Stderr, "Confirm these exact effects by typing %q: ", phrase)
			if err = confirmApp(os.Stdin, phrase); err != nil {
				return err
			}
			*approval = workload.Digest(workload.JSON(p))
		}
		// Re-read all authored/artifact/instance bindings after confirmation.
		fresh, _, err := appworkspace.Load(root)
		if err != nil {
			return err
		}
		if workload.Digest(workload.JSON(fresh.Config)) != p.ConfigSHA256 || workload.Digest(workload.JSON(fresh.Model.Intent)) != p.IntentSHA256 {
			return errors.New("inputs changed after review; nothing deployed")
		}
		fresh.Progress = w.Progress
		if err = fresh.LoadBase(ctx); err != nil {
			return err
		}
		if err = fresh.Deploy(ctx, p, *approval); err != nil {
			return fmt.Errorf("deployment stopped: %w; run app status --workspace %s; preserve intents/receipts, do not replay partial writes or clear STOP", err, root)
		}
		result := map[string]string{"deployment": "DEPLOYED", "functional": "UNPROVEN", "target": p.Cluster, "planSHA256": *approval}
		if *jsonOutput {
			fmt.Print(string(workload.JSON(result)))
		} else {
			fmt.Println("DEPLOYED: converged and ready. Business functionality: UNPROVEN.")
			fmt.Println("Next: app status; app open --service <name>; run your business tests.")
		}
		return nil
	}
	if err = w.LoadBase(ctx); err != nil {
		return err
	}
	switch verb {
	case "status":
		s, e := w.Current(ctx)
		if *jsonOutput {
			fmt.Print(string(workload.JSON(s)))
		} else {
			fmt.Printf("Instance: %s\nGitOps: %s @ %s\nAuthority: %s\nCurrent: %s (%s @ %s)\nRecorded result: %s; functionality: %s\nServices: %s\nCredentials (location only): %s\n", s.Target, s.Repository, s.Branch, s.Authority, s.Current, s.Phase, s.Revision, s.HistoricalResult, s.Functional, strings.Join(s.Services, ", "), s.CredentialsPath)
			if s.UnpublishedPlan != "" {
				fmt.Println("Unpublished review:", s.UnpublishedPlan, "(observing the previous deployment)")
			}
			for _, n := range s.Next {
				fmt.Println("Next:", n)
			}
		}
		return e
	case "open":
		return w.OpenApplication(ctx, *service, *port, func(s string) { fmt.Println(s) })
	case "logs":
		return w.ApplicationLogs(ctx, *service, os.Stdout)
	}
	return errors.New("unsupported app operation")
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func confirmApp(in io.Reader, want string) error {
	line, err := bufio.NewReader(io.LimitReader(in, 1024)).ReadString('\n')
	if err != nil || strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") != want {
		return errors.New("confirmation declined; no deployment performed")
	}
	return nil
}
func appPlan(ctx context.Context, w *workloadrun.Workflow, root, config string) (workloadrun.Plan, workloadrun.Preview, error) {
	var p workloadrun.Plan
	var v workloadrun.Preview
	if err := w.PlanningAllowed(); err != nil {
		return p, v, err
	}
	if err := w.PrepareRepository(ctx); err != nil {
		return p, v, err
	}
	if err := w.LoadBase(ctx); err != nil {
		return p, v, err
	}
	p, err := w.Plan(ctx)
	if err != nil {
		return p, v, err
	}
	v, err = w.Preview(ctx, p)
	if err != nil {
		return p, v, err
	}
	if err = w.SavePlan(p); err != nil {
		return p, v, err
	}
	if err = appworkspace.Record(root, config, p); err != nil {
		return p, v, err
	}
	return p, v, nil
}
func printAppPlan(p workloadrun.Plan, v workloadrun.Preview, jsonOutput bool) error {
	if jsonOutput {
		fmt.Print(string(workload.JSON(struct {
			Plan   workloadrun.Plan    `json:"plan"`
			Impact workloadrun.Preview `json:"impact"`
		}{p, v})))
		return nil
	}
	fmt.Printf("Target: %s (UID %s)\nGitOps: %s / %s\nProject: %s\nPhases: %s\n", v.Target, p.ClusterUID, v.Repository, v.Branch, v.After.Project.Name, strings.Join(v.Phases, " -> "))
	old := map[string]workload.Workload{}
	if v.Before != nil {
		for _, w := range v.Before.Workloads {
			old[w.Name] = w
		}
	}
	var cpu, memory, steadyCPU, steadyMemory int64
	for _, w := range v.After.Workloads {
		prior, ok := old[w.Name]
		if ok {
			fmt.Printf("  %s image: %s -> %s\n    replicas: %d -> %d; requests: %+v -> %+v; limits: %+v -> %+v\n", w.Name, prior.Image, w.Image, prior.Replicas, w.Replicas, prior.Resources.Requests, w.Resources.Requests, prior.Resources.Limits, w.Resources.Limits)
		} else {
			fmt.Printf("  Create %s: %s; replicas=%d; requests=%+v limits=%+v\n", w.Name, w.Image, w.Replicas, w.Resources.Requests, w.Resources.Limits)
		}
		fmt.Printf("    TLS: %s; metrics=%t\n", w.Exposure.Hostname, w.Observability.Metrics)
		steadyCPU += w.Replicas * w.Resources.Requests.CPU
		steadyMemory += w.Replicas * w.Resources.Requests.Memory
		cpu += (w.Replicas + 1) * w.Resources.Requests.CPU
		memory += (w.Replicas + 1) * w.Resources.Requests.Memory
	}
	fmt.Printf("Steady requests: %dm CPU, %d MiB memory\n", steadyCPU, steadyMemory)
	fmt.Printf("Rolling peak requests: %dm CPU, %d MiB memory; project quota: %dm, %d MiB\n", cpu, memory, v.After.Project.Quota.RequestsCPU, v.After.Project.Quota.RequestsMemory)
	for _, b := range v.After.Bindings {
		fmt.Printf("Binding: %s -> %s/%s (%s)\n", b.Workload, b.Capability, b.Bucket, b.Access)
	}
	fmt.Println("Credential ciphertext targets:", strings.Join(v.CredentialTargets, ", "))
	for _, op := range v.Operations {
		fmt.Println("Effect:", op)
	}
	fmt.Println("No Bootstrap authority changes, deletion, credential rotation or functional writes.")
	fmt.Println("Plan SHA256:", v.PlanSHA256)
	return nil
}
