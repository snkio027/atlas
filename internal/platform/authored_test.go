package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelmValuesRejectAmbiguityBeforeRendering(t *testing.T) {
	for _, input := range []string{"image: a\nimage: b\n", "image: &value pinned\nother: *value\n", "image: pinned\n---\nimage: other\n"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "values.yaml"), []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		p := &Project{Root: root}
		if _, err := p.inputPath("values.yaml"); err == nil {
			t.Fatal("ambiguous Helm values reached renderer")
		}
	}
}

func TestHelmReceivesCanonicalScalarMeaning(t *testing.T) {
	helm := os.Getenv("ATLAS_TEST_HELM")
	if helm == "" {
		t.Skip("locked Helm required")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"Chart.yaml":            "apiVersion: v2\nname: scalar-test\nversion: 0.1.0\n",
		"input.yaml":            "word: on\ntext: no\nactual: true\n",
		"templates/result.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: result\ndata:\n  value: {{ .Values | toJson | quote }}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := &Project{Root: root, Tools: Tools{Helm: helm}}
	b, err := p.helmTemplate(context.Background(), []string{"template", "test", root, "--values", filepath.Join(root, "input.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`\"word\":\"on\"`, `\"text\":\"no\"`, `\"actual\":true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("scalar changed through Helm: %s", b)
		}
	}
}
