// atlas-install is the D1 release entrypoint. The adjacent runtime manifest is
// content-bound at build time; it is not located through the caller's checkout.
package main

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/installation"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var productDigest string

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("usage: atlas-install prepare|plan|install|verify|access --config <installation.yaml>")
		return nil
	}
	if len(args) == 0 {
		return errors.New("usage: atlas-install prepare|plan|install|verify|access --config <installation.yaml>")
	}
	verb := args[0]
	switch verb {
	case "prepare", "plan", "install", "verify", "access":
	default:
		return errors.New("unknown installation command")
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	config := fs.String("config", "installation.yaml", "installation configuration")
	approval := fs.String("approve-plan", "", "exact reviewed plan SHA256")
	service := fs.String("service", "web", "web, grafana, prometheus, alertmanager or s3")
	functional := fs.Bool("functional", false, "repeat bounded S3/alert test writes during verification")
	if e := fs.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *approval != "" && verb != "install" || *functional && verb != "verify" {
		return errors.New("flag is not valid for this command")
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	executable, e = filepath.EvalSymlinks(executable)
	if e != nil {
		return e
	}
	manifest, e := os.ReadFile(filepath.Join(filepath.Dir(executable), "runtime.json"))
	if e != nil {
		return e
	}
	if productDigest == "" || installation.Digest(manifest) != productDigest {
		return errors.New("runtime manifest differs from the release binary; use a verified complete package")
	}
	var product installation.Product
	if e = installation.Decode(manifest, &product); e != nil {
		return e
	}
	if filepath.Ext(*config) != ".yaml" {
		return errors.New("installation configuration must use .yaml")
	}
	b, e := authored.Read(*config)
	if e != nil {
		return e
	}
	var c installation.Config
	if e = installation.Decode(b, &c); e != nil {
		return e
	}
	binary, e := os.ReadFile(executable)
	if e != nil {
		return e
	}
	w := installation.Workflow{Config: c, Product: product, ProductDigest: productDigest, BinaryDigest: installation.Digest(binary), Progress: func(s string) { fmt.Println(s) }}
	if e = w.Open(verb == "prepare"); e != nil {
		return e
	}
	lock := w.Lock
	if verb == "access" || verb == "verify" && !*functional {
		lock = w.ReadLock
	}
	unlock, e := lock()
	if e != nil {
		return e
	}
	defer unlock()
	if e = w.Open(false); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	timeout := 90 * time.Minute
	if verb == "access" {
		timeout = 24 * time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch verb {
	case "prepare":
		return w.Prepare(ctx)
	case "plan":
		if e = w.CheckPlan(ctx); e != nil {
			return e
		}
		fmt.Print(string(w.PlanJSON()))
		fmt.Println("Plan SHA256:", installation.Digest(w.PlanJSON()))
		return nil
	case "install":
		e = w.Install(ctx, *approval)
	case "verify":
		e = w.Verify(ctx, *functional)
	case "access":
		return w.Access(ctx, *service)
	}
	if e != nil {
		return e
	}
	fmt.Println(w.Summary())
	return nil
}
