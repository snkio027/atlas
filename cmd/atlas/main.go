package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: atlas <doctor|render|status|apply> --config <file> [options]")
		return 2
	}
	if args[0] == "--help" || args[0] == "help" {
		fmt.Println("atlas doctor|render|status|apply\n--config config.local.json --root . --tool-dir <preinstalled tools>\nrender: --output gitops/test\napply: --approve-cluster <exact name> --approve-tier0\nOnly isolated OrbStack/Kind test clusters are supported.")
		return 0
	}
	if args[0] == "--version" {
		fmt.Println("atlas-refactor 0.1.0-dev")
		return 0
	}
	command := args[0]
	if command != "doctor" && command != "render" && command != "status" && command != "apply" {
		fmt.Fprintln(os.Stderr, "unknown command")
		return 2
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	config := fs.String("config", "config.local.json", "repository-relative configuration file")
	root := fs.String("root", ".", "repository directory")
	tools := fs.String("tool-dir", "", "directory of preinstalled locked tools")
	var output, approved string
	var tier0 bool
	if command == "render" {
		fs.StringVar(&output, "output", ".state/rendered", "repository-relative output directory")
	}
	if command == "apply" {
		fs.StringVar(&approved, "approve-cluster", "", "exact cluster name")
		fs.BoolVar(&tier0, "approve-tier0", false, "approve initial Tier-0 instantiation")
	}
	if command == "status" {
		fs.Bool("check", false, "explicit strict status check (status always uses strict exit codes)")
	}
	if e := fs.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	abs, e = filepath.EvalSymlinks(abs)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	c, l, e := atlas.Load(abs, *config)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	app := atlas.App{Root: abs, Config: c, Lock: l, Runner: atlas.ExecRunner{Root: abs, ToolDir: *tools, DockerContext: c.DockerContext}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	switch command {
	case "doctor":
		e = app.Doctor(ctx)
	case "render":
		var files map[string][]byte
		files, e = app.Render(ctx)
		if e == nil {
			e = atlas.WriteFiles(abs, output, files)
		}
	case "status":
		report := app.Status(ctx)
		_ = json.NewEncoder(os.Stdout).Encode(report)
		switch report.State {
		case atlas.Adopted:
			return 0
		case atlas.Unavailable, atlas.Drifted:
			return 2
		default:
			return 1
		}
	case "apply":
		e = app.Apply(ctx, approved, tier0)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	fmt.Println(command + ": OK")
	return 0
}
func main() { os.Exit(run(os.Args[1:])) }
