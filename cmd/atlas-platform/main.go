// atlas-platform compiles manifests, publishes explicitly approved business
// intent, and observes deployments. It does not acquire Bootstrap authority.
package main

import (
	"atlas-refactor/internal/platform"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "management-access" {
		if err := runManagementAccess(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "app" {
		if e := runApp(os.Args[2:]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "workload" {
		if e := runWorkload(os.Args[2:]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "observe" || os.Args[1] == "verify") {
		os.Exit(runObservation(os.Args[2:]))
	}
	fs := flag.NewFlagSet("atlas-platform", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	helm := fs.String("helm", "helm", "locked Helm executable")
	kubectl := fs.String("kubectl", "kubectl", "locked kubectl (client commands only)")
	cert := fs.String("cert", "", "reviewed public Sealed Secrets certificate (offline)")
	kubeseal := fs.String("kubeseal", "kubeseal", "checksum-locked kubeseal executable")
	credentialProfile := fs.String("credential-profile", "platform", "fixed offline credential recipe: platform or data-services")
	capabilities := fs.String("capabilities", "monitoring,object-storage,storage-monitoring", "complete desired capability set for plan/select; removal is unsupported")
	yq := fs.String("yq", "yq", "locked yq executable")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Everyday application delivery: atlas-platform app --help\nAdvanced platform commands: atlas-platform render|check|plan|select|prepare-credentials [flags] (local files only; legacy flags-first accepted)")
		fs.PrintDefaults()
	}
	command, err := parseLocalCommand(fs, os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fs.Usage()
		os.Exit(2)
	}
	p, e := platform.Load(*root, platform.Tools{Helm: *helm, Kubectl: *kubectl, YQ: *yq})
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 3*time.Minute)
	defer cancel()

	requested := []string{}
	if *capabilities != "" {
		requested = strings.Split(*capabilities, ",")
	}
	if command == "prepare-credentials" {
		e = p.PrepareCredentialProfile(ctx, *cert, *kubeseal, *credentialProfile)
		if e == nil {
			fmt.Println("Prepared sealed manifests; plaintext remains in the private .state profile directory. No cluster operations.")
		}
	} else if command == "select" {
		e = p.SelectCapabilities(ctx, requested)
		if e == nil {
			fmt.Println("Updated local capability selection and GitOps projection. Review the diff before publishing.")
		}
	} else if command == "plan" {
		var plan platform.Object
		plan, e = p.CapabilityPlan(requested)
		if e == nil {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			e = encoder.Encode(plan)
		}
	} else if command == "render" {
		plan, err := p.CapabilityPlan(p.Capabilities.Enabled.Capabilities)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err = p.ValidateCapabilitySelection(p.Capabilities.Active); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var files map[string][]byte
		files, e = p.Render(ctx)
		if e == nil {
			e = p.Write(files)
		}

		if e == nil {
			files, e = p.CapabilityActivation(p.Capabilities.Active)
			if e == nil {
				e = p.Write(files)
			}
		}
		if e == nil {
			e = p.ValidateCapabilityProjection()
		}
		if e == nil {
			fmt.Printf("Rendered templates and catalog; activationReady=%v. Run plan before activation. No cluster operations.\n", plan["readyToEnable"])
		}
	} else {
		var n int
		n, e = p.Check(ctx)
		if e == nil {
			fmt.Printf("Checked %d GitOps resources, artifact hashes, deterministic renders, project boundaries, CRD structural schemas and local Kustomize builds. Activation credentials are checked separately by plan/select/Bootstrap. No cluster operations.\n", n)
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

// Accept the same verb-first convention as the other CLIs while preserving
// existing scripts that put all flags before the verb. Do not accept trailing
// arguments: silently ignored options can change the requested capability set.
func parseLocalCommand(fs *flag.FlagSet, args []string) (string, error) {
	valid := func(command string) bool {
		switch command {
		case "render", "check", "plan", "select", "prepare-credentials":
			return true
		}
		return false
	}
	command := ""
	if len(args) > 0 && valid(args[0]) {
		command, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if command == "" && fs.NArg() == 1 && valid(fs.Arg(0)) {
		return fs.Arg(0), nil
	}
	if command != "" && fs.NArg() == 0 {
		return command, nil
	}
	return "", errors.New("expected one local command and its flags")
}
