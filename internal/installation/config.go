// Package installation implements the bounded D1 local installation path.
// It deliberately does not implement upgrade, retirement or recovery.
package installation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

const RootPath = "gitops/root/overlays/development"
const SourceRepository = "https://github.com/snkio027/atlas.git"
const SourceRevision = "codex/development-platform"
const CredentialPath = "gitops/platform/management/platform-credentials/overlays/development/resources.json"

type Config struct {
	Schema          int    `json:"schema"`
	Cluster         string `json:"cluster"`
	Repository      string `json:"repository"`
	Branch          string `json:"branch"`
	HTTPPort        int    `json:"httpPort"`
	HTTPSPort       int    `json:"httpsPort"`
	StateDirectory  string `json:"stateDirectory"`
	BackupDirectory string `json:"backupDirectory"`
	BackupIsolation string `json:"backupIsolation"`
}

var sha = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commit = regexp.MustCompile(`^[a-f0-9]{40}$`)
var repository = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.git$`)
var cluster = regexp.MustCompile(`^atlas-[a-z0-9](?:[a-z0-9-]{0,34}[a-z0-9])?$`)
var branch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_/-]{0,99}$`)

func (c Config) Validate() error {
	if c.Schema != 1 || !cluster.MatchString(c.Cluster) || strings.HasPrefix(c.Cluster, "atlas-refactor-test") {
		return errors.New("D1 requires schema 1 and a new atlas-<name>; frozen test names are reserved")
	}
	if !repository.MatchString(c.Repository) {
		return errors.New("use a public GitHub HTTPS repository and a new dedicated deployment branch")
	}
	if !branch.MatchString(c.Branch) || strings.Contains(c.Branch, "//") || strings.HasSuffix(c.Branch, "/") || strings.HasPrefix(c.Branch, "codex/") {
		return errors.New("use a dedicated deployment branch without reserved codex/ prefix")
	}
	if c.HTTPPort < 1024 || c.HTTPPort > 65535 || c.HTTPSPort < 1024 || c.HTTPSPort > 65535 || c.HTTPPort == c.HTTPSPort {
		return errors.New("two distinct unprivileged loopback ports are required")
	}
	for _, p := range []string{c.StateDirectory, c.BackupDirectory} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return errors.New("state and backup paths must be absolute clean directories")
		}
	}
	if within(c.StateDirectory, c.BackupDirectory) || within(c.BackupDirectory, c.StateDirectory) {
		return errors.New("backup and installation state must be separate directories")
	}
	if c.BackupIsolation != "external" && c.BackupIsolation != "same-host-development-exception" {
		return errors.New("declare external backup or an explicit same-host-development-exception")
	}
	return nil
}
func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}
func Digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func JSON(v any) []byte {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		panic(e)
	}
	return append(b, '\n')
}

// Reject duplicate keys, unknown struct fields and trailing input at every
// configuration boundary. JSON is never evaluated as executable configuration.
func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
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
				if !ok || seen[s] {
					return fmt.Errorf("duplicate/invalid key %q", s)
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = value(); e != nil {
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
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
