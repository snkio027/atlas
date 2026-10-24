package workloadrun

import (
	"errors"
	"github.com/snkio027/atlas/internal/workload"
	"slices"
	"strings"
	"testing"
)

func TestImageImportRegistersCRINameAndChecksAuthoredReference(t *testing.T) {
	const node = "atlas-d1-r2-control-plane"
	digest := strings.Repeat("a", 64)
	ref := "atlas.local/s2-web:v1@sha256:" + digest
	canonical := "atlas.local/s2-web@sha256:" + digest
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new canonical name", true: "existing same canonical name"}[existing], func(t *testing.T) {
			registered := existing
			tags, imports, inspections := 0, 0, 0
			run := func(input []byte, args ...string) ([]byte, error) {
				if !slices.Contains(args, node) || slices.Contains(args, "--force") {
					t.Fatal("wrong target or force overwrite", args)
				}
				switch {
				case slices.Contains(args, "import"):
					imports++
					if string(input) != "verified archive" {
						t.Fatal("archive lost")
					}
				case slices.Contains(args, "list"):
					if args[len(args)-1] != "target.digest==sha256:"+digest {
						t.Fatal("unbound source lookup")
					}
					// Reproduce the partial first attempt: the literal ref exists,
					// but CRI resolves tag+digest through the canonical key only.
					refs := "atlas.local/s2-web:v1\n" + ref + "\n"
					if registered {
						refs += canonical + "\n"
					}
					return []byte(refs), nil
				case slices.Contains(args, "tag"):
					tags++
					if args[len(args)-2] != "atlas.local/s2-web:v1" || args[len(args)-1] != canonical {
						t.Fatal("noncanonical alias")
					}
					registered = true
				case slices.Contains(args, "inspecti"):
					inspections++
					if args[len(args)-1] != ref || !registered {
						return nil, errors.New("CRI cannot resolve authored ref")
					}
					return workload.JSON(Object{"status": Object{"repoDigests": []string{canonical}}}), nil
				default:
					t.Fatal("unexpected command", args)
				}
				return nil, nil
			}
			if err := importWebImage(run, node, []byte("verified archive"), ref); err != nil {
				t.Fatal(err)
			}
			wantTags := 1
			if existing {
				wantTags = 0
			}
			if imports != 1 || inspections != 1 || tags != wantTags {
				t.Fatal("unexpected writes or skipped proof", imports, tags, inspections)
			}
		})
	}
}

func TestImageImportFailsClosedAtEveryBoundary(t *testing.T) {
	digest := strings.Repeat("a", 64)
	ref := "atlas.local/s2-web:v1@sha256:" + digest
	for _, failure := range []string{"invalid-ref", "import", "list", "wrong-source", "tag", "inspecti", "malformed", "foreign-repository", "wrong-digest"} {
		t.Run(failure, func(t *testing.T) {
			failed := false
			run := func(input []byte, args ...string) ([]byte, error) {
				if failed {
					t.Fatal("continued after failure")
				}
				for _, step := range []string{"import", "list", "tag", "inspecti"} {
					if failure == step && slices.Contains(args, step) {
						failed = true
						return nil, errors.New("injected failure")
					}
				}
				if slices.Contains(args, "list") {
					if failure == "wrong-source" {
						failed = true
						return []byte("unrelated.local/image:v1"), nil
					}
					return []byte("atlas.local/s2-web:v1"), nil
				}
				if slices.Contains(args, "inspecti") {
					failed = true
					switch failure {
					case "malformed":
						return []byte("not json"), nil
					case "foreign-repository":
						return workload.JSON(Object{"status": Object{"repoDigests": []string{"foreign.local/image@sha256:" + digest}}}), nil
					case "wrong-digest":
						return workload.JSON(Object{"status": Object{"repoDigests": []string{"atlas.local/s2-web@sha256:" + strings.Repeat("b", 64)}}}), nil
					}
				}
				return nil, nil
			}
			image := ref
			if failure == "invalid-ref" {
				image = "atlas.local/s2-web:v1"
				failed = true
			}
			if err := importWebImage(run, "test-node", nil, image); err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
}
