package workloadrun

import (
	"bytes"
	"context"
	"github.com/snkio027/atlas/internal/workload"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationAccessRequiresCompleteExactWritableRepository(t *testing.T) {
	valid := Object{"full_name": "owner/project", "private": false, "archived": false, "push": true}
	if err := validatePublicationAccess("owner/project", workload.JSON(valid)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"full_name", "private", "archived", "push"} {
		t.Run("missing "+field, func(t *testing.T) {
			x := Object{}
			for k, v := range valid {
				x[k] = v
			}
			delete(x, field)
			if validatePublicationAccess("owner/project", workload.JSON(x)) == nil {
				t.Fatal("missing evidence accepted")
			}
		})
	}
	for name, raw := range map[string]string{
		"foreign repository": `{"full_name":"other/project","private":false,"archived":false,"push":true}`,
		"private":            `{"full_name":"owner/project","private":true,"archived":false,"push":true}`,
		"archived":           `{"full_name":"owner/project","private":false,"archived":true,"push":true}`,
		"denied":             `{"full_name":"owner/project","private":false,"archived":false,"push":false}`,
		"unknown push":       `{"full_name":"owner/project","private":false,"archived":false,"push":null}`,
		"duplicate":          `{"full_name":"owner/project","private":false,"archived":false,"push":false,"push":true}`,
		"not JSON":           `login required`,
	} {
		t.Run(name, func(t *testing.T) {
			if validatePublicationAccess("owner/project", []byte(raw)) == nil {
				t.Fatal("unavailable or wrong authority accepted")
			}
		})
	}
}

func TestPublicationUsesGHStoreWithoutChangingUserGitConfiguration(t *testing.T) {
	d := t.TempDir()
	home := filepath.Join(d, "home")
	bin := filepath.Join(d, "bin")
	for _, p := range []string{home, bin} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	global := []byte("[credential]\n\thelper = !exit 99\n[commit]\n\tgpgsign = true\n[core]\n\thooksPath = /untrusted/hooks\n")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), global, 0600); err != nil {
		t.Fatal(err)
	}
	// This fixture never accesses the user's gh store. The helper emits only
	// synthetic data and rejects every operation except Git credential lookup.
	script := []byte("#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = git-credential ] && [ \"$3\" = get ]; then\n  printf 'username=fixture\\npassword=fixture-only-token\\n'\nelse\n  exit 17\nfi\n")
	if err := os.WriteFile(filepath.Join(bin, "gh"), script, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w := &Workflow{Config: Config{StateDirectory: filepath.Join(d, "state")}}
	if err := os.MkdirAll(w.repo(), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := w.git(ctx, nil, "init", "--bare"); err != nil {
		t.Fatal(err)
	}
	credential, err := w.git(ctx, []byte("protocol=https\nhost=github.com\npath=owner/project.git\n\n"), "credential", "fill")
	if err != nil || !bytes.Contains(credential, []byte("password=fixture-only-token")) {
		t.Fatal("explicit gh helper was not selected")
	}
	for key, want := range map[string]string{"core.hooksPath": "/dev/null", "commit.gpgsign": "false"} {
		got, err := w.git(ctx, nil, "config", "--get", key)
		if err != nil || strings.TrimSpace(string(got)) != want {
			t.Fatal("unsafe process Git policy", key)
		}
	}
	tree, err := w.git(ctx, nil, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := w.git(ctx, []byte("fixture base\n"), "commit-tree", strings.TrimSpace(string(tree)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.commit(ctx, Files{"platform/projects/demo.json": []byte("{}\n")}, strings.TrimSpace(string(parent)), "fixture update"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil || !bytes.Equal(global, after) {
		t.Fatal("user Git configuration changed")
	}
	local, err := os.ReadFile(filepath.Join(w.repo(), "config"))
	if err != nil || bytes.Contains(local, []byte("fixture-only-token")) {
		t.Fatal("credential persisted in repository configuration")
	}
}
