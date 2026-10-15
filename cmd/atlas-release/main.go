// atlas-release builds, but never publishes, a complete D1 archive. It is a
// maintainer/CI tool and is not required by installation users.
package main

import (
	"archive/tar"
	"atlas-refactor/internal/installation"
	"atlas-refactor/internal/platform"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", ".", "source root")
	output := flag.String("output", ".state/latest/d1-package", "output directory")
	version := flag.String("version", "", "release version")
	toolDir := flag.String("tool-dir", ".state/tools", "render tool directory")
	prepareOnly := flag.Bool("prepare-tools", false, "prepare locked runtime tools only; output is private cache")
	development := flag.Bool("development", false, "permit dirty-tree package for local tests; never publication")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	abs, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	out, e := filepath.Abs(*output)
	if e != nil {
		return e
	}
	tools, e := filepath.Abs(*toolDir)
	if e != nil {
		return e
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || runtime.Version() != "go1.27.2" {
		return errors.New("release build requires locked go1.27.2 on darwin/arm64")
	}
	if *prepareOnly {
		b, e := os.ReadFile(filepath.Join(abs, "packaging/tools-darwin-arm64.json"))
		if e != nil {
			return e
		}
		var locks []installation.Tool
		if e = installation.Decode(b, &locks); e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		if e = installation.PrepareTools(ctx, out, locks); e != nil {
			return e
		}
		fmt.Println(installation.ToolDirectory(out, locks))
		return nil
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = abs
		b, e := cmd.Output()
		return strings.TrimSpace(string(b)), e
	}
	status, e := git("status", "--porcelain", "--untracked-files=all")
	if e != nil {
		return e
	}
	if status != "" && !*development {
		return errors.New("release build requires a clean committed checkout")
	}
	source, e := git("rev-parse", "HEAD")
	if e != nil {
		return e
	}
	if *development && !strings.Contains(*version, "-dev") {
		return errors.New("dirty-tree packages require an explicit -dev version")
	}
	b, e := os.ReadFile(filepath.Join(abs, "packaging/tools-darwin-arm64.json"))
	if e != nil {
		return e
	}
	var locks []installation.Tool
	if e = installation.Decode(b, &locks); e != nil {
		return e
	}
	product, e := installation.BuildProduct(abs, *version, source, platform.Tools{Helm: filepath.Join(tools, "helm"), Kubectl: filepath.Join(tools, "kubectl"), YQ: filepath.Join(tools, "yq")}, locks)
	if e != nil {
		return e
	}
	manifest := installation.JSON(product)
	digest := installation.Digest(manifest)
	temp, e := os.MkdirTemp("", "atlas-release-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(temp)
	binary := filepath.Join(temp, "atlas-install")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-buildid= -X main.productDigest="+digest, "-o", binary, "./cmd/atlas-install")
	cmd.Dir = abs
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=vendor")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if e = cmd.Run(); e != nil {
		return e
	}
	executable, e := os.ReadFile(binary)
	if e != nil {
		return e
	}
	files := map[string][]byte{"atlas-install": executable, "runtime.json": manifest}
	for _, f := range []struct{ from, to string }{{"LICENSE", "LICENSE"}, {"docs/first-install.md", "README.md"}, {"packaging/installation.example.yaml", "installation.example.yaml"}, {"packaging/NOTICES.md", "NOTICES.md"}, {"vendor/platform/LICENSE-Apache-2.0", "licenses/Apache-2.0.txt"}, {"vendor/go.yaml.in/yaml/v3/LICENSE", "licenses/go-yaml.txt"}, {"vendor/go.yaml.in/yaml/v3/NOTICE", "licenses/go-yaml-NOTICE.txt"}} {
		b, e = os.ReadFile(filepath.Join(abs, f.from))
		if e != nil {
			return e
		}
		files[f.to] = b
	}
	b, e = os.ReadFile(filepath.Join(abs, "packaging/licenses/Go-BSD.txt"))
	if e != nil {
		return e
	}
	files["licenses/Go-BSD.txt"] = b
	files["build.json"] = installation.JSON(map[string]any{"version": *version, "sourceCommit": source, "go": runtime.Version(), "platform": "darwin/arm64", "productSHA256": digest, "developmentOnly": *development, "binarySHA256": installation.Digest(executable)})
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var checks strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&checks, "%s  %s\n", installation.Digest(files[k]), k)
	}
	files["SHA256SUMS"] = []byte(checks.String())
	keys = append(keys, "SHA256SUMS")
	sort.Strings(keys)
	if e = os.MkdirAll(out, 0700); e != nil {
		return e
	}
	name := "atlas-" + *version + "-darwin-arm64.tar.gz"
	path := filepath.Join(out, name)
	fd, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		fd.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	h := new(bytes.Buffer)
	gz := gzip.NewWriter(io.MultiWriter(fd, h))
	tw := tar.NewWriter(gz)
	for _, k := range keys {
		mode := int64(0600)
		if k == "atlas-install" {
			mode = 0700
		}
		if e = tw.WriteHeader(&tar.Header{Name: k, Mode: mode, Size: int64(len(files[k])), ModTime: time.Unix(0, 0)}); e != nil {
			return e
		}
		if _, e = tw.Write(files[k]); e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = fd.Sync(); e != nil {
		return e
	}
	if e = fd.Close(); e != nil {
		return e
	}
	ok = true
	checksum := []byte(installation.Digest(h.Bytes()) + "  " + name + "\n")
	if e = os.WriteFile(path+".sha256", checksum, 0600); e != nil {
		return e
	}
	fmt.Println(path)
	fmt.Printf("SHA256 %s\n", installation.Digest(h.Bytes()))
	return nil
}
