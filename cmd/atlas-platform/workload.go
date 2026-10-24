package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/snkio027/atlas/internal/workload"
	"github.com/snkio027/atlas/internal/workloadrun"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func runWorkload(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: atlas-platform workload plan|compile|deploy|prepare-credentials|baseline|publish|observe|probe --config s2.json")
	}
	verb := args[0]
	valid := map[string]bool{"deploy": true, "plan": true, "compile": true, "prepare-credentials": true, "baseline": true, "publish": true, "observe": true, "probe": true}
	if !valid[verb] {
		return errors.New("unknown workload command")
	}
	f := flag.NewFlagSet("workload "+verb, flag.ContinueOnError)
	config := f.String("config", "s2.json", "private S2 configuration")
	phase := f.String("phase", "infrastructure", "permissions, project, infrastructure or consumer")
	approval := f.String("approve-plan", "", "exact approved plan SHA256")
	revision := f.String("revision", "", "exact deployed commit for observation")
	wait := f.Duration("wait", 0, "bounded read-only convergence wait, at most 15m")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 || *wait < 0 || *wait > 15*time.Minute {
		return errors.New("unexpected argument or wait outside 0..15m")
	}
	var flagError error
	f.Visit(func(v *flag.Flag) {
		allowed := v.Name == "config" || v.Name == "approve-plan" || v.Name == "phase" && (verb == "compile" || verb == "publish" || verb == "observe") || (v.Name == "revision" || v.Name == "wait") && verb == "observe"
		if !allowed {
			flagError = fmt.Errorf("--%s is not valid for workload %s", v.Name, verb)
		}
	})
	if flagError != nil {
		return flagError
	}
	mutation := verb == "prepare-credentials" || verb == "baseline" || verb == "publish" || verb == "probe" || verb == "deploy"
	if mutation && *approval == "" || !mutation && *approval != "" {
		return errors.New("exact plan approval is required only for explicit writes/credential use")
	}
	w, e := workloadrun.Load(*config)
	if e != nil {
		return e
	}
	w.Progress = func(s string) { fmt.Fprintln(os.Stderr, s) }
	unlock, e := w.Lock()
	if e != nil {
		return e
	}
	defer unlock()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	if verb == "plan" {
		if e = w.PrepareRepository(ctx); e != nil {
			return e
		}
	}
	if e = w.LoadBase(ctx); e != nil {
		return e
	}
	if verb == "plan" {
		p, e := w.Plan(ctx)
		if e != nil {
			return e
		}
		if e = w.SavePlan(p); e != nil {
			return e
		}
		fmt.Print(string(workload.JSON(p)))
		fmt.Println("Plan SHA256:", workload.Digest(workload.JSON(p)))
		return nil
	}
	if verb == "compile" {
		r, e := w.Compile(*phase)
		if e != nil {
			return e
		}
		if e = w.WriteResult(r); e != nil {
			return e
		}
		fmt.Printf("Compiled %s: %d uniquely owned resources; no live operations.\n", *phase, len(r.Inventory.Resources))
		return nil
	}
	if verb == "observe" {
		r, e := w.Compile(*phase)
		if e != nil {
			return e
		}
		report, e := w.ObserveFor(ctx, r, *revision, *wait)
		if e != nil {
			return e
		}
		fmt.Print(string(workload.JSON(report)))
		return nil
	}
	p, e := w.ReadPlan()
	if e != nil {
		return e
	}
	switch verb {
	case "deploy":
		return w.Deploy(ctx, p, *approval)
	case "baseline":
		return w.CaptureBaseline(ctx, p, *approval)
	case "prepare-credentials":
		return w.PrepareCredentials(ctx, p, *approval)
	case "publish":
		r, e := w.Publish(ctx, p, *approval, *phase)
		if e != nil {
			return e
		}
		fmt.Print(string(workload.JSON(r)))
		return nil
	case "probe":
		return w.Probe(ctx, p, *approval)
	}
	return errors.New("unknown workload operation")
}
