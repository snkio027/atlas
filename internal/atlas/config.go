package atlas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/developmentprofile"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	Schema         int    `json:"schema"`
	Cluster        string `json:"cluster"`
	RepositoryURL  string `json:"repositoryURL"`
	Revision       string `json:"revision"`
	GitOpsPath     string `json:"gitopsPath"`
	DockerContext  string `json:"dockerContext"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type Lock struct {
	Schema      int               `json:"schema"`
	Go          string            `json:"go"`
	Helm        string            `json:"helm"`
	Kind        string            `json:"kind"`
	Kubectl     string            `json:"kubectl"`
	Kubernetes  string            `json:"kubernetes"`
	Chart       string            `json:"chart"`
	ChartSHA256 string            `json:"chartSHA256"`
	NodeImage   string            `json:"nodeImage"`
	ArgoImage   string            `json:"argoImage"`
	RedisImage  string            `json:"redisImage"`
	Assets      map[string]string `json:"assets"`
}

// Reject duplicate keys before decoding structs; encoding/json alone accepts them.
func strictJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok {
					return errors.New("invalid object key")
				}
				if seen[s] {
					return fmt.Errorf("duplicate JSON key %q", s)
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(); e != nil {
					return e
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON input")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func jsonBytes(v any) []byte {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		panic(e)
	}
	return append(b, '\n')
}

func safePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", errors.New("path must be a clean repository-relative path")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink forbidden: %s", relative)
		}
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
	}
	return current, nil
}

func readFile(root, relative string) ([]byte, error) {
	p, e := safePath(root, relative)
	if e != nil {
		return nil, e
	}
	i, e := os.Stat(p)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() {
		return nil, errors.New("input must be a regular file")
	}
	return os.ReadFile(p)
}

func Load(root, configFile string) (Config, Lock, error) {
	var c Config
	var l Lock
	b, e := readFile(root, configFile)
	if e != nil {
		return c, l, e
	}
	if e = strictJSON(b, &c); e != nil {
		return c, l, e
	}
	b, e = readFile(root, "versions.lock.json")
	if e != nil {
		return c, l, e
	}
	if e = strictJSON(b, &l); e != nil {
		return c, l, e
	}
	if e = c.Validate(); e != nil {
		return c, l, e
	}
	return c, l, l.Validate()
}

func (c Config) Validate() error {
	if (c.Cluster == developmentprofile.OT1Cluster || c.Revision == developmentprofile.OT1Revision) && c.Schema != 3 {
		return errors.New("OT-1 requires the isolated four-node profile")
	}
	if c.Schema == 4 {
		if !regexp.MustCompile(`^atlas-[a-z0-9](?:[a-z0-9-]{0,34}[a-z0-9])?$`).MatchString(c.Cluster) || strings.HasPrefix(c.Cluster, "atlas-refactor-test") {
			return errors.New("invalid new installation cluster")
		}
	} else if (c.Schema != 1 && c.Schema != 2 && c.Schema != 3) || !regexp.MustCompile(`^atlas-refactor-test(?:-[a-z0-9]{1,12})?$`).MatchString(c.Cluster) {
		return errors.New("schema 1, 2 or 3 and an atlas-refactor-test[-suffix] cluster are required")
	}
	u, e := url.Parse(c.RepositoryURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, ".git") {
		return errors.New("repositoryURL must be a public HTTPS Git URL without credentials, query or fragment")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,150}$`).MatchString(c.Revision) || strings.Contains(c.Revision, "..") {
		return errors.New("invalid revision")
	}
	if c.DockerContext != "orbstack" || c.TimeoutSeconds < 30 || c.TimeoutSeconds > 1800 {
		return errors.New("orbstack and a 30..1800 second timeout are required")
	}
	if c.Schema == 4 {
		if u.Host != "github.com" || c.GitOpsPath != developmentprofile.RootPath || strings.HasPrefix(c.Revision, "codex/") {
			return errors.New("invalid user deployment binding")
		}
	} else if c.developmentProfile() {
		if e := developmentprofile.Validate(c.Schema, c.Cluster, c.RepositoryURL, c.Revision, c.GitOpsPath); e != nil {
			return e
		}
	} else if c.GitOpsPath != "gitops/test" {
		return errors.New("schema 1 requires gitops/test")
	}
	return nil
}
func (l Lock) Validate() error {
	exact := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	for _, v := range []string{l.Go, l.Helm, l.Kind, l.Kubectl, l.Kubernetes} {
		if !exact.MatchString(v) {
			return errors.New("tool versions must be exact patch versions")
		}
	}
	hash := regexp.MustCompile(`^[a-f0-9]{64}$`)
	image := regexp.MustCompile(`^[^\s]+:[^\s@:]+@sha256:[a-f0-9]{64}$`)
	chart := regexp.MustCompile(`^vendor/charts/argo-cd-[0-9]+\.[0-9]+\.[0-9]+\.tgz$`)
	if l.Schema != 1 || !chart.MatchString(l.Chart) || !hash.MatchString(l.ChartSHA256) {
		return errors.New("invalid chart lock")
	}
	for _, v := range []string{l.NodeImage, l.ArgoImage, l.RedisImage} {
		if !image.MatchString(v) || strings.Contains(v, ":latest@") {
			return errors.New("image must be version and digest pinned")
		}
	}
	if len(l.Assets) != 2 {
		return errors.New("exactly two rendering assets must be locked")
	}
	for _, p := range []string{"assets/argocd-values.yaml", "assets/argocd-cm.yaml"} {
		if !hash.MatchString(l.Assets[p]) {
			return errors.New("missing asset checksum")
		}
	}
	return nil
}

func (c Config) Identity() map[string]string {
	// Timeout is an execution preference, not part of cluster ownership.
	return map[string]string{"schema": "github.com/snkio027/atlas/identity/v1", "cluster": c.Cluster, "repositoryURL": c.RepositoryURL, "revision": c.Revision, "gitopsPath": c.GitOpsPath, "dockerContext": c.DockerContext}
}
