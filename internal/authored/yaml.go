// Package authored accepts a deliberately small YAML data language. Runtime
// plans, receipts, locks and generated Kubernetes objects continue to use JSON.
package authored

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var number = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// JSON rejects syntax that can hide input or depend on a parser's coercions.
// Conversion precedes the existing strict typed JSON boundary; it never adds
// missing defaults, evaluates expressions, or resolves environment variables.
func JSON(data []byte) ([]byte, error) {
	if len(data) > 16<<20 {
		return nil, errors.New("authored input exceeds 16 MiB")
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := d.Decode(&root); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("authored input must contain exactly one document")
	}
	var visit func(*yaml.Node, int) (any, error)
	visit = func(n *yaml.Node, depth int) (any, error) {
		fail := func(s string) (any, error) { return nil, fmt.Errorf("line %d column %d: %s", n.Line, n.Column, s) }
		if depth > 100 {
			return fail("nesting exceeds 100 levels")
		}
		if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Style&yaml.TaggedStyle != 0 {
			return fail("anchors, aliases and explicit tags are forbidden")
		}
		switch n.Kind {
		case yaml.DocumentNode:
			if len(n.Content) != 1 {
				return fail("empty document")
			}
			return visit(n.Content[0], depth+1)
		case yaml.MappingNode:
			m := map[string]any{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || key.Anchor != "" || key.Style&yaml.TaggedStyle != 0 {
					return fail("mapping keys must be plain strings; merge keys are forbidden")
				}
				if _, ok := m[key.Value]; ok {
					return nil, fmt.Errorf("line %d column %d: duplicate key %q", key.Line, key.Column, key.Value)
				}
				v, err := visit(n.Content[i+1], depth+1)
				if err != nil {
					return nil, err
				}
				m[key.Value] = v
			}
			return m, nil
		case yaml.SequenceNode:
			a := make([]any, 0, len(n.Content))
			for _, x := range n.Content {
				v, e := visit(x, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			return a, nil
		case yaml.ScalarNode:
			switch n.Tag {
			case "!!str":
				return n.Value, nil
			case "!!null":
				if n.Value != "null" {
					return fail("use explicit null, not an empty value or ~")
				}
				return nil, nil
			case "!!bool":
				if n.Value == "true" {
					return true, nil
				}
				if n.Value == "false" {
					return false, nil
				}
				return fail("booleans must be true or false")
			case "!!int", "!!float":
				if !number.MatchString(n.Value) {
					return fail("numbers must use JSON decimal syntax")
				}
				return json.Number(n.Value), nil
			default:
				return fail("unsupported scalar type; quote dates and other strings")
			}
		}
		return fail("unsupported YAML node")
	}
	v, err := visit(&root, 0)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Resolve supports frozen JSON inputs without allowing two competing sources.
// Callers retain their stable logical input paths; current authored files use
// .yaml, while old immutable fixtures retain their original .json bytes.
func Resolve(path string) (string, error) {
	if filepath.Ext(path) != ".json" && filepath.Ext(path) != ".yaml" {
		return path, nil
	}
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	found := ""
	for _, ext := range []string{".json", ".yaml", ".yml"} {
		p := stem + ext
		st, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return "", e
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("authored input is not a regular file: %s", p)
		}
		if found != "" {
			return "", fmt.Errorf("conflicting authored sources: %s and %s", found, p)
		}
		if ext == ".yml" {
			return "", fmt.Errorf("use the .yaml extension: %s", p)
		}
		found = p
	}
	if found == "" {
		return "", &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return found, nil
}
func Read(path string) ([]byte, error) {
	p, e := Resolve(path)
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, e
	}
	if filepath.Ext(p) == ".yaml" {
		b, e = JSON(b)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", p, e)
		}
	}
	return b, nil
}
func YAML(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var n yaml.Node
	if e = yaml.Unmarshal(b, &n); e != nil {
		return nil, e
	}
	var clean func(*yaml.Node)
	clean = func(n *yaml.Node) {
		n.Style = 0
		for _, c := range n.Content {
			clean(c)
		}
	}
	clean(&n)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if e = enc.Encode(&n); e != nil {
		return nil, e
	}
	if e = enc.Close(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
