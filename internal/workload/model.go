// Package workload compiles typed business intent. It has no Kubernetes or Git
// mutation authority; provider details exist only in the final adapters.
package workload

import (
	"atlas-refactor/internal/authored"
	"atlas-refactor/internal/platformnames"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

type Grant struct {
	Capability string `json:"capability"`
	Bucket     string `json:"bucket"`
	Access     string `json:"access"`
}
type Quota struct {
	Pods           int64 `json:"pods"`
	RequestsCPU    int64 `json:"requestsCpuMillicores"`
	LimitsCPU      int64 `json:"limitsCpuMillicores"`
	RequestsMemory int64 `json:"requestsMemoryMiB"`
	LimitsMemory   int64 `json:"limitsMemoryMiB"`
}
type Project struct {
	Schema           int     `json:"schema"`
	Kind             string  `json:"kind"`
	Name             string  `json:"name"`
	Owner            string  `json:"owner"`
	Quota            Quota   `json:"quota"`
	CapabilityAccess []Grant `json:"capabilityAccess"`
}
type Quantity struct {
	CPU    int64 `json:"cpuMillicores"`
	Memory int64 `json:"memoryMiB"`
}
type Resources struct {
	Requests Quantity `json:"requests"`
	Limits   Quantity `json:"limits"`
}
type Exposure struct {
	Hostname string `json:"hostname"`
	TLS      bool   `json:"tls"`
}
type Observability struct {
	Metrics bool `json:"metrics"`
}
type Workload struct {
	Schema        int           `json:"schema"`
	Kind          string        `json:"kind"`
	Type          string        `json:"type"`
	Project       string        `json:"project"`
	Name          string        `json:"name"`
	Image         string        `json:"image"`
	Port          int           `json:"port"`
	Replicas      int64         `json:"replicas"`
	Resources     Resources     `json:"resources"`
	Exposure      Exposure      `json:"exposure"`
	Observability Observability `json:"observability"`
}
type Binding struct {
	Schema     int    `json:"schema"`
	Kind       string `json:"kind"`
	Project    string `json:"project"`
	Name       string `json:"name"`
	Workload   string `json:"workload"`
	Capability string `json:"capability"`
	Bucket     string `json:"bucket"`
	Access     string `json:"access"`
}
type Intent struct {
	Project   Project    `json:"project"`
	Workloads []Workload `json:"workloads"`
	Bindings  []Binding  `json:"bindings"`
}
type ResolvedWorkload struct {
	Definition Workload
	Binding    *Binding
}
type Model struct {
	Intent    Intent
	Workloads []ResolvedWorkload
}

var nameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,18}[a-z0-9])?$`)
var imageRE = regexp.MustCompile(`^[a-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9_./-]+:[A-Za-z0-9_.-]+@sha256:[a-f0-9]{64}$`)
var shaRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func JSON(v any) []byte {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		panic(e)
	}
	return append(b, '\n')
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func ShortID(kind string, parts ...string) string {
	a := append([]string{kind}, parts...)
	b, _ := json.Marshal(a)
	return Digest(b)[:12]
}
func (p Project) Namespace() string  { return platformnames.Project(p.Name) }
func (w Workload) Namespace() string { return platformnames.Project(w.Project) }
func (b Binding) Namespace() string  { return platformnames.Project(b.Project) }
func (b Binding) ID() string         { return ShortID("CapabilityBinding", b.Project, b.Name) }
func (b Binding) Secret() string     { return "s3-" + b.ID() }
func (w Workload) AppName() string {
	return "s2-w-" + w.Project + "-" + w.Name + "-" + ShortID("Workload", w.Project, w.Name)
}
func (p Project) AppName() string { return "s2-p-" + p.Name + "-" + ShortID("Project", p.Name) }
func (b Binding) AppName() string { return "s2-b-" + b.Project + "-" + b.Name + "-" + b.ID() }

// StrictDecode checks the full token stream, then exact field spelling and
// presence. encoding/json alone accepts duplicate keys and case aliases.
func StrictDecode(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var visit func() error
	visit = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		if t == nil {
			return errors.New("null is forbidden")
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := t.(string)
				if !ok || seen[k] {
					return errors.New("duplicate JSON key")
				}
				seen[k] = true
				if e = visit(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := visit(); e != nil {
					return e
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := visit(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer {
		return errors.New("decode requires pointer")
	}
	var shape func(json.RawMessage, reflect.Type) error
	shape = func(raw json.RawMessage, t reflect.Type) error {
		if t.Kind() == reflect.Pointer {
			return shape(raw, t.Elem())
		}
		switch t.Kind() {
		case reflect.Struct:
			var fields map[string]json.RawMessage
			if e := json.Unmarshal(raw, &fields); e != nil {
				return e
			}
			known := map[string]reflect.StructField{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				key := strings.Split(tag, ",")[0]
				if key == "" {
					key = f.Name
				}
				known[key] = f
				if _, ok := fields[key]; !ok && !strings.Contains(tag, ",omitempty") {
					return fmt.Errorf("missing field %s", key)
				}
			}
			for k, v := range fields {
				f, ok := known[k]
				if !ok {
					return fmt.Errorf("unknown field %s", k)
				}
				if e := shape(v, f.Type); e != nil {
					return fmt.Errorf("%s: %w", k, e)
				}
			}
		case reflect.Slice:
			var xs []json.RawMessage
			if e := json.Unmarshal(raw, &xs); e != nil {
				return e
			}
			for _, v := range xs {
				if e := shape(v, t.Elem()); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := shape(b, t.Elem()); e != nil {
		return e
	}
	return json.Unmarshal(b, dst)
}
func validGrant(g Grant) bool { return g == (Grant{"object-storage", "uploads", "read-write"}) }
func validQuantity(q Quantity) bool {
	return q.CPU > 0 && q.CPU <= 1000000000 && q.Memory >= 64 && q.Memory <= 1000000000
}
func Resolve(in Intent, objectStorage bool) (*Model, error) {
	// Round-trip clones caller slices; sorting cannot mutate caller state.
	var c Intent
	if e := StrictDecode(JSON(in), &c); e != nil {
		return nil, e
	}
	p := c.Project
	if p.Schema != 1 || p.Kind != "Project" || !nameRE.MatchString(p.Name) || platformnames.Reserved(p.Name) || strings.HasPrefix(p.Name, "kube-") || strings.HasPrefix(p.Name, "atlas-") {
		return nil, errors.New("invalid/reserved Project identity")
	}
	if strings.TrimSpace(p.Owner) != p.Owner || len(p.Owner) == 0 || len(p.Owner) > 128 || strings.ContainsAny(p.Owner, "\r\n\x00") {
		return nil, errors.New("invalid Project owner")
	}
	q := p.Quota
	if q.Pods < 1 || q.Pods > 1024 || !validQuantity(Quantity{q.RequestsCPU, q.RequestsMemory}) || !validQuantity(Quantity{q.LimitsCPU, q.LimitsMemory}) || q.RequestsCPU > q.LimitsCPU || q.RequestsMemory > q.LimitsMemory {
		return nil, errors.New("invalid Project quota")
	}
	if len(c.Workloads) == 0 || len(c.Workloads) > 64 {
		return nil, errors.New("require 1..64 WebServices")
	}
	grants := map[Grant]bool{}
	for _, g := range p.CapabilityAccess {
		if !validGrant(g) || grants[g] || !objectStorage {
			return nil, errors.New("invalid/unavailable capability grant")
		}
		grants[g] = true
	}
	sort.Slice(c.Workloads, func(i, j int) bool { return c.Workloads[i].Name < c.Workloads[j].Name })
	sort.Slice(c.Bindings, func(i, j int) bool { return c.Bindings[i].Name < c.Bindings[j].Name })
	names := map[string]bool{}
	hosts := map[string]bool{"web.atlas.test": true}
	total := Quota{}
	for _, w := range c.Workloads {
		if w.Schema != 1 || w.Kind != "Workload" || w.Type != "WebService" || w.Project != p.Name || !nameRE.MatchString(w.Name) || names[w.Name] {
			return nil, errors.New("invalid/duplicate Workload identity or project reference")
		}
		names[w.Name] = true
		if !imageRE.MatchString(w.Image) || w.Port < 1024 || w.Port > 65535 || w.Replicas < 1 || w.Replicas > 128 {
			return nil, fmt.Errorf("platform/workloads/%s/%s.yaml: image / port / replicas: require a pinned image, port 1024..65535 and replicas 1..128", w.Project, w.Name)
		}
		h, ok := strings.CutSuffix(w.Exposure.Hostname, ".atlas.test")
		if !ok || !nameRE.MatchString(h) || hosts[w.Exposure.Hostname] || !w.Exposure.TLS {
			return nil, fmt.Errorf("platform/workloads/%s/%s.yaml: exposure: unique <name>.atlas.test hostname with tls=true required", w.Project, w.Name)
		}
		hosts[w.Exposure.Hostname] = true
		r := w.Resources
		if !validQuantity(r.Requests) || !validQuantity(r.Limits) || r.Requests.CPU > r.Limits.CPU || r.Requests.Memory > r.Limits.Memory {
			return nil, fmt.Errorf("platform/workloads/%s/%s.yaml: resources: positive requests must fit limits (memory >= 64 MiB)", w.Project, w.Name)
		}
		n := w.Replicas + 1
		total.Pods += n
		total.RequestsCPU += n * r.Requests.CPU
		total.LimitsCPU += n * r.Limits.CPU
		total.RequestsMemory += n * r.Requests.Memory
		total.LimitsMemory += n * r.Limits.Memory
	}
	if total.Pods > q.Pods || total.RequestsCPU > q.RequestsCPU || total.LimitsCPU > q.LimitsCPU || total.RequestsMemory > q.RequestsMemory || total.LimitsMemory > q.LimitsMemory {
		return nil, fmt.Errorf("platform/projects/%s.yaml: quota: rolling peak %+v exceeds budget %+v; lower Workload requests/limits/replicas or review Project quota", p.Name, total, q)
	}
	bindings := map[string]*Binding{}
	bn := map[string]bool{}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		g := Grant{b.Capability, b.Bucket, b.Access}
		if b.Schema != 1 || b.Kind != "CapabilityBinding" || b.Project != p.Name || !nameRE.MatchString(b.Name) || bn[b.Name] || bindings[b.Workload] != nil || !names[b.Workload] || !grants[g] || !objectStorage {
			return nil, fmt.Errorf("platform/bindings/%s/%s.yaml: project / workload / capability / bucket / access: invalid, duplicate or unauthorized Binding reference", b.Project, b.Name)
		}
		bn[b.Name] = true
		bindings[b.Workload] = b
	}
	m := &Model{Intent: c}
	for _, w := range c.Workloads {
		m.Workloads = append(m.Workloads, ResolvedWorkload{w, bindings[w.Name]})
	}
	return m, nil
}
func ValidateUpdate(old, next *Model) error {
	a, b := old.Intent, next.Intent
	if a.Project.Name != b.Project.Name || a.Project.Owner != b.Project.Owner {
		return errors.New("Project identity update is unsupported")
	}
	for _, g := range a.Project.CapabilityAccess {
		found := false
		for _, v := range b.Project.CapabilityAccess {
			found = found || v == g
		}
		if !found {
			return errors.New("capability revocation is unsupported")
		}
	}
	wm := map[string]Workload{}
	for _, w := range b.Workloads {
		wm[w.Name] = w
	}
	for _, w := range a.Workloads {
		n, ok := wm[w.Name]
		if !ok || w.Project != n.Project || w.Type != n.Type || w.Port != n.Port || w.Exposure != n.Exposure || w.Observability.Metrics && !n.Observability.Metrics {
			return errors.New("Workload retirement or identity change is unsupported")
		}
	}
	bm := map[string]Binding{}
	for _, v := range b.Bindings {
		bm[v.Name] = v
	}
	for _, v := range a.Bindings {
		if bm[v.Name] != v {
			return errors.New("Binding removal/rebinding is unsupported")
		}
	}
	return nil
}
func ReadIntent(root string) (Intent, error) {
	abs, e := filepath.Abs(root)
	if e != nil {
		return Intent{}, e
	}
	real, e := filepath.EvalSymlinks(abs)
	if e != nil || real != abs {
		return Intent{}, errors.New("intent root traverses symlinks")
	}
	root = abs
	var in Intent
	in.Workloads = []Workload{}
	in.Bindings = []Binding{}
	projects := 0
	for _, dir := range []string{"projects", "workloads", "bindings"} {
		base := filepath.Join(root, "platform", dir)
		e := filepath.WalkDir(base, func(path string, d os.DirEntry, e error) (resultErr error) {
			defer func() {
				if resultErr != nil {
					resultErr = fmt.Errorf("%s: %w", path, resultErr)
				}
			}()
			if os.IsNotExist(e) && path == base {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return errors.New("symlink intent is forbidden")
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() || (filepath.Ext(path) != ".json" && filepath.Ext(path) != ".yaml") {
				return errors.New("unexpected intent file")
			}
			raw, e := authored.Read(path)
			if e != nil {
				return e
			}
			var expected string
			switch dir {
			case "projects":
				var p Project
				if e = StrictDecode(raw, &p); e != nil {
					return e
				}
				projects++
				in.Project = p
				expected = filepath.Join(base, p.Name+filepath.Ext(path))
			case "workloads":
				var w Workload
				if e = StrictDecode(raw, &w); e != nil {
					return e
				}
				in.Workloads = append(in.Workloads, w)
				expected = filepath.Join(base, w.Project, w.Name+filepath.Ext(path))
			case "bindings":
				var b Binding
				if e = StrictDecode(raw, &b); e != nil {
					return e
				}
				in.Bindings = append(in.Bindings, b)
				expected = filepath.Join(base, b.Project, b.Name+filepath.Ext(path))
			}
			if path != expected {
				return errors.New("intent filename differs from identity")
			}
			return nil
		})
		if e != nil {
			return in, e
		}
	}
	if projects != 1 {
		return in, errors.New("S2 requires exactly one Project")
	}
	return in, nil
}
func (m *Model) Authored() map[string][]byte {
	in := m.Intent
	r := map[string][]byte{"platform/projects/" + in.Project.Name + ".json": JSON(in.Project)}
	for _, w := range in.Workloads {
		r["platform/workloads/"+w.Project+"/"+w.Name+".json"] = JSON(w)
	}
	for _, b := range in.Bindings {
		r["platform/bindings/"+b.Project+"/"+b.Name+".json"] = JSON(b)
	}
	return r
}

func (m *Model) HumanAuthored() (map[string][]byte, error) {
	result := map[string][]byte{}
	for name, raw := range m.Authored() {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		b, err := authored.YAML(v)
		if err != nil {
			return nil, err
		}
		result[strings.TrimSuffix(name, ".json")+".yaml"] = b
	}
	return result, nil
}
