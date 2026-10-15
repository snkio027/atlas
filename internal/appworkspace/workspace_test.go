package appworkspace

import (
	"atlas-refactor/internal/workload"
	"atlas-refactor/internal/workloadrun"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestWorkspaceFilesRejectSymlinkAndDoNotOverwrite(t *testing.T) {
	d := privateTemp(t)
	path := filepath.Join(d, "proof")
	if err := Write(path, []byte("first"), true); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second"), true); err == nil {
		t.Fatal("overwrote create-only file")
	}
	alias := filepath.Join(d, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(alias, false); err == nil {
		t.Fatal("read symlink")
	}
	if err := Write(alias, []byte("evil"), false); err == nil {
		t.Fatal("wrote symlink")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "first" {
		t.Fatal(string(b))
	}
}
func TestRecordedConfigCannotEscapeWorkspace(t *testing.T) {
	d := privateTemp(t)
	p := reviewPlan("escape")
	digest := workload.Digest(workload.JSON(p))
	if err := Write(filepath.Join(d, ".atlas/reviews", digest+".json"), workload.JSON(Review{"/unrelated/config.json", p}), true); err != nil {
		t.Fatal(err)
	}
	if _, err := readReview(d, digest); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatal(err)
	}
}
func TestWorkspaceStrictConfigAndRelativeArtifact(t *testing.T) {
	d := privateTemp(t)
	path := filepath.Join(d, "app.yaml")
	raw := workload.JSON(Config{Schema: 1, InstallationConfig: "relative", InstallationPackage: "/pkg", ProductSource: "/src", Artifact: "/image"})
	if err := Write(path, raw, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigAt(d); err == nil {
		t.Fatal("relative target accepted")
	}
	if err := Write(path, []byte(strings.Replace(string(raw), "\"schema\": 1", "\"schema\": 1, \"context\": \"current\"", 1)), false); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigAt(d); err == nil {
		t.Fatal("context escape accepted")
	}
}

func reviewPlan(tag string) workloadrun.Plan {
	return workloadrun.Plan{Schema: 2, Project: tag, CompilerSHA256: "compiler", PhaseSHA256: map[string]string{}, CredentialTargets: []string{}, Phases: []string{"consumer"}}
}
func TestUnpublishedReviewObservesPreviousDeploymentButPartialAttemptDoesNot(t *testing.T) {
	d := privateTemp(t)
	first := reviewPlan("first")
	old := workload.Digest(workload.JSON(first))
	config := filepath.Join(d, ".atlas/configs/config.json")
	if err := Record(d, config, first); err != nil {
		t.Fatal(err)
	}
	final := workload.JSON(map[string]string{"planSHA256": old, "result": "DEPLOYED"})
	if err := Write(filepath.Join(d, ".atlas/run/authority", old, "final.json"), final, true); err != nil {
		t.Fatal(err)
	}
	next := reviewPlan("next")
	newDigest := workload.Digest(workload.JSON(next))
	if err := Record(d, config, next); err != nil {
		t.Fatal(err)
	}
	got, draft, err := recordedReview(d)
	if err != nil || got.Plan.Project != "first" || draft != newDigest {
		t.Fatal(got, draft, err)
	}
	if err = Write(filepath.Join(d, ".atlas/run/authority", newDigest, "consumer.json.intent"), []byte("{}"), true); err != nil {
		t.Fatal(err)
	}
	got, draft, err = recordedReview(d)
	if err != nil || got.Plan.Project != "next" || draft != "" {
		t.Fatal("partial attempt hidden by old success", got, draft, err)
	}
	if _, err = Read(filepath.Join(d, ".atlas/reviews", old+".json"), true); err != nil {
		t.Fatal("lost executed plan", err)
	}
}

func TestInstanceBindingAcceptsPublicD1LayoutAndRejectsAmbiguity(t *testing.T) {
	for _, layout := range []string{"root", "nested", "ambiguous", "missing"} {
		t.Run(layout, func(t *testing.T) {
			d := privateTemp(t)
			if err := Write(filepath.Join(d, "installation.yaml"), []byte("{}"), true); err != nil {
				t.Fatal(err)
			}
			if layout == "root" || layout == "ambiguous" {
				if err := Write(filepath.Join(d, "runtime.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			}
			if layout == "nested" || layout == "ambiguous" {
				if err := Write(filepath.Join(d, "package/runtime.json"), []byte("{}"), true); err != nil {
					t.Fatal(err)
				}
			}
			_, pkg, err := instancePaths(d)
			if (err == nil) != (layout == "root" || layout == "nested") {
				t.Fatal(layout, pkg, err)
			}
			if layout == "root" && pkg != d || layout == "nested" && pkg != filepath.Join(d, "package") {
				t.Fatal(pkg)
			}
		})
	}
}
