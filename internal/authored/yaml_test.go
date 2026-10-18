package authored

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictYAML(t *testing.T) {
	for _, s := range []string{"a: 1\na: 2", "a: {b: 1, b: 2}", "a: &value hi\nb: *value", "a: !!str 1", "a: {<<: {b: 1}}", "true: 1", "a: 01", "a: .inf", "a: 2026-10-16", "a:", "a: ~", "a: TRUE", "a: 1\n---\nb: 2"} {
		if _, e := JSON([]byte(s)); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	b, e := JSON([]byte("# comment\nname: \"001\"\nboolean: false\nlist: [1, 2]\nscript: |\n  echo ok\n"))
	if e != nil || !strings.Contains(string(b), `"name": "001"`) {
		t.Fatal(string(b), e)
	}
}
func TestRoundtripDoesNotChangeStrings(t *testing.T) {
	for _, v := range []any{map[string]any{"date": "2026-10-16", "number": "001", "bool": "true", "null": "null", "empty": "", "int": 12, "multiline": "a\nb\n"}} {
		b, e := YAML(v)
		if e != nil {
			t.Fatal(e)
		}
		j, e := JSON(b)
		if e != nil {
			t.Fatal(string(b), e)
		}
		again, e := YAML(v)
		if e != nil || string(b) != string(again) {
			t.Fatal("nondeterministic", e)
		}
		if !strings.Contains(string(j), `"number": "001"`) {
			t.Fatal(string(j))
		}
	}
}
func TestConflictingSourcesAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.yaml")
	if e := os.WriteFile(path, []byte("x: 1"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "x.json"), []byte(`{"x":1}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Read(path); e == nil {
		t.Fatal("two sources accepted")
	}
	if e := os.Symlink(path, filepath.Join(dir, "link.yaml")); e != nil {
		t.Fatal(e)
	}
	if _, e := Read(filepath.Join(dir, "link.yaml")); e == nil {
		t.Fatal("symlink accepted")
	}
}
