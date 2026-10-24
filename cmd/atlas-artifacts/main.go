// atlas-artifacts is an online preparation tool, with no Kubernetes authority.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/snkio027/atlas/internal/atlas"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "prepare" {
		fmt.Fprintln(os.Stderr, "usage: atlas-artifacts prepare --config profiles/development.json --tool-dir <tools>")
		os.Exit(2)
	}
	f := flag.NewFlagSet("prepare", flag.ExitOnError)
	root := f.String("root", ".", "repository")
	config := f.String("config", "profiles/development.json", "profile")
	tools := f.String("tool-dir", "", "locked tools")
	_ = f.Parse(os.Args[2:])
	if f.NArg() != 0 {
		os.Exit(2)
	}
	abs, e := filepath.Abs(*root)
	if e == nil {
		abs, e = filepath.EvalSymlinks(abs)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	c, l, e := atlas.Load(abs, *config)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	a := atlas.App{Root: abs, Config: c, Lock: l, Runner: atlas.ExecRunner{Root: abs, ToolDir: *tools, DockerContext: c.DockerContext}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if e = a.PrepareImages(ctx); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Println("artifacts: OK")
}
