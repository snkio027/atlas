package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

type credentialTool struct {
	Version          string `json:"version"`
	ExecutableSHA256 string `json:"executableSHA256"`
	Platform         string `json:"platform"`
	ArchiveSHA256    string `json:"archiveSHA256"`
	Source           string `json:"source"`
}
type localCredentials struct {
	GrafanaPassword string `json:"grafanaPassword"`
	AccessKey       string `json:"accessKey"`
	SecretKey       string `json:"secretKey"`
}
type dataServiceCredentials struct {
	PostgresPassword     string `json:"postgresPassword"`
	RedisPassword        string `json:"redisPassword"`
	FlinkWebhookPassword string `json:"flinkWebhookPassword"`
}
type sealingReceipt struct {
	CertificateSHA256 string `json:"certificateSHA256"`
	CredentialsSHA256 string `json:"credentialsSHA256"`
	OutputSHA256      string `json:"outputSHA256"`
}

// PrepareCredentials has no Kubernetes client. A reviewed controller supplies
// only its PUBLIC certificate. Plaintext stays in a create-only private file;
// stdin to the pinned kubeseal process contains the transient Secret objects.
func (p *Project) PrepareCredentials(ctx context.Context, certPath, kubeseal string) error {
	return p.PrepareCredentialProfile(ctx, certPath, kubeseal, "platform")
}

// PrepareCredentialProfile keeps data-service credentials independent from D1.
// Profiles are a fixed allowlist, never user-supplied Secret templates.
func (p *Project) PrepareCredentialProfile(ctx context.Context, certPath, kubeseal, profile string) error {
	if profile != "platform" && profile != "data-services" {
		return errors.New("unknown credential profile")
	}
	var lock credentialTool
	if e := readJSON(filepath.Join(p.Root, capabilityDir, "kubeseal.lock.json"), &lock); e != nil {
		return e
	}
	if lock.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return errors.New("kubeseal lock does not cover this host platform")
	}
	executable, e := exec.LookPath(kubeseal)
	if e != nil {
		return e
	}
	b, e := readInput(executable)
	if e != nil {
		return e
	}
	if hash(b) != lock.ExecutableSHA256 {
		return errors.New("kubeseal executable checksum mismatch")
	}
	cert, e := readInput(certPath)
	if e != nil {
		return e
	}
	block, rest := pem.Decode(cert)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("expected exactly one public PEM certificate")
	}
	certificate, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return e
	}
	key, ok := certificate.PublicKey.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 4096 || time.Now().Before(certificate.NotBefore) || !time.Now().Before(certificate.NotAfter) {
		return errors.New("invalid, expired, or weak sealing certificate")
	}
	privateDir := filepath.Join(p.Root, ".state/capabilities")
	if profile == "data-services" {
		privateDir = filepath.Join(p.Root, ".state/data-services")
	}
	// Never follow a pre-existing symlink into another location.
	for _, dir := range []string{filepath.Join(p.Root, ".state"), privateDir} {
		if st, e := os.Lstat(dir); e == nil && (st.Mode()&os.ModeSymlink != 0 || !st.IsDir()) {
			return errors.New("unsafe private state directory")
		}
	}
	if e = os.MkdirAll(privateDir, 0700); e != nil {
		return e
	}
	if e = os.Chmod(privateDir, 0700); e != nil {
		return e
	}
	privatePath := filepath.Join(privateDir, "credentials.json")
	var credentials localCredentials
	var dataCredentials dataServiceCredentials
	var record any = &credentials
	if profile == "data-services" {
		record = &dataCredentials
	}
	if st, e := os.Lstat(privatePath); e == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
			return errors.New("credentials file must be a regular 0600 file")
		}
		if e = readJSON(privatePath, record); e != nil {
			return e
		}
	} else if os.IsNotExist(e) {
		random := func() (string, error) {
			b := make([]byte, 32)
			_, e := rand.Read(b)
			return base64.RawURLEncoding.EncodeToString(b), e
		}
		if profile == "data-services" {
			if dataCredentials.PostgresPassword, e = random(); e != nil {
				return e
			}
			if dataCredentials.RedisPassword, e = random(); e != nil {
				return e
			}
			if dataCredentials.FlinkWebhookPassword, e = random(); e != nil {
				return e
			}
		} else {
			if credentials.GrafanaPassword, e = random(); e != nil {
				return e
			}
			if credentials.AccessKey, e = random(); e != nil {
				return e
			}
			if credentials.SecretKey, e = random(); e != nil {
				return e
			}
		}
		b, _ := json.MarshalIndent(record, "", "  ")
		f, e := os.OpenFile(privatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(append(b, '\n'))
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else {
		return e
	}
	if profile == "platform" && (len(credentials.GrafanaPassword) < 32 || len(credentials.AccessKey) < 32 || len(credentials.SecretKey) < 32) || profile == "data-services" && (len(dataCredentials.PostgresPassword) < 32 || len(dataCredentials.RedisPassword) < 32 || len(dataCredentials.FlinkWebhookPassword) < 32) {
		return errors.New("invalid generated credentials")
	}
	privateBytes, e := readInput(privatePath)
	if e != nil {
		return e
	}
	receiptPath := filepath.Join(privateDir, "sealing-receipt.json")
	outputPath := capabilityDir + "/resources/platform-credentials.json"
	if profile == "data-services" {
		outputPath = capabilityDir + "/resources/data-credentials.json"
	}
	current, e := readInput(filepath.Join(p.Root, outputPath))
	if e != nil {
		return e
	}
	var receipt sealingReceipt
	if e = readJSON(receiptPath, &receipt); e == nil && receipt.CertificateSHA256 == hash(cert) && receipt.CredentialsSHA256 == hash(privateBytes) && receipt.OutputSHA256 == hash(current) {
		return nil
	}
	objects, e := decodeObjects(current)
	if e != nil {
		return e
	}
	if len(objects) > 0 {
		return errors.New("existing sealed credentials have no matching local receipt; preserve them and use a separately reviewed rotation")
	}
	s3config, _ := json.Marshal(Object{"identities": []any{Object{"name": "web-uploads", "credentials": []any{Object{"accessKey": credentials.AccessKey, "secretKey": credentials.SecretKey}}, "actions": []string{"Read:uploads", "Write:uploads", "List:uploads", "Tagging:uploads"}}}})
	definitions := []struct {
		namespace, name string
		data            map[string]string
	}{
		{p.Capabilities.Catalog.Components["monitoring"].Namespace, "grafana-admin", map[string]string{"admin-user": "admin", "admin-password": credentials.GrafanaPassword}},
		{p.Capabilities.Catalog.Components["object-storage"].Namespace, "seaweedfs-auth", map[string]string{"seaweedfs_s3_config": string(s3config)}},
		{p.namespace("workload-web"), "s3-client", map[string]string{"AWS_ACCESS_KEY_ID": credentials.AccessKey, "AWS_SECRET_ACCESS_KEY": credentials.SecretKey}},
	}
	if profile == "data-services" {
		passwordHash := sha256.Sum256([]byte(dataCredentials.RedisPassword))
		definitions = definitions[:0]
		definitions = append(definitions, struct {
			namespace, name string
			data            map[string]string
		}{p.namespace("postgresql"), "postgresql-app", map[string]string{"username": "platform", "password": dataCredentials.PostgresPassword}})
		definitions = append(definitions, struct {
			namespace, name string
			data            map[string]string
		}{p.namespace("redis"), "redis-auth", map[string]string{"password": dataCredentials.RedisPassword, "users.acl": "user default off\nuser platform-admin on #" + hex.EncodeToString(passwordHash[:]) + " ~* &* +@all\n"}})
		definitions = append(definitions, struct {
			namespace, name string
			data            map[string]string
		}{p.operatorNamespace("flink-operator", "flink"), "flink-webhook-password", map[string]string{"password": dataCredentials.FlinkWebhookPassword}})
	}
	sealed := []Object{}
	certFile, e := os.CreateTemp(privateDir, "public-cert-*.pem")
	if e != nil {
		return e
	}
	defer os.Remove(certFile.Name())
	if _, e = certFile.Write(cert); e != nil {
		certFile.Close()
		return e
	}
	if e = certFile.Close(); e != nil {
		return e
	}
	for _, d := range definitions {
		data := Object{}
		for k, v := range d.data {
			data[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		secret := Object{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": Object{"namespace": d.namespace, "name": d.name}, "data": data}
		if profile == "data-services" && d.namespace == p.namespace("postgresql") && d.name == "postgresql-app" {
			secret["type"] = "kubernetes.io/basic-auth"
		}
		input, _ := json.Marshal(secret)
		cmd := exec.CommandContext(ctx, executable, "--cert", certFile.Name(), "--scope", "strict", "--format", "json")
		cmd.Stdin = bytes.NewReader(input)
		output, e := cmd.Output()
		if e != nil {
			return errors.New("kubeseal encryption failed; child output suppressed")
		}
		var o Object
		if e = json.Unmarshal(output, &o); e != nil {
			return errors.New("invalid kubeseal response")
		}
		if o["kind"] != "SealedSecret" || field(o, "metadata", "namespace") != d.namespace || field(o, "metadata", "name") != d.name {
			return errors.New("kubeseal changed credential identity")
		}
		metadata(o)["annotations"] = Object{"argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}
		delete(metadata(o), "creationTimestamp")
		delete(o, "status")
		templateMeta := mapping(field(o, "spec", "template", "metadata"))
		delete(templateMeta, "creationTimestamp")
		sealed = append(sealed, o)
	}
	result, _ := json.MarshalIndent(Object{"apiVersion": "v1", "kind": "List", "items": sealed}, "", "  ")
	result = append(result, '\n')
	if e = p.Write(map[string][]byte{outputPath: result}); e != nil {
		return e
	}
	receipt = sealingReceipt{hash(cert), hash(privateBytes), hash(result)}
	b, _ = json.MarshalIndent(receipt, "", "  ")
	if e = os.WriteFile(receiptPath, append(b, '\n'), 0600); e != nil {
		return fmt.Errorf("ciphertext saved but private receipt failed: %w", e)
	}
	return nil
}

// SelectCapabilities changes only the local declarative selection and its two
// Git projections. Publishing the resulting diff remains a separate action.
func (p *Project) SelectCapabilities(ctx context.Context, requested []string) error {
	names, e := p.ResolveCapabilities(requested)
	if e != nil {
		return e
	}
	if e = p.ValidateCapabilitySelection(names); e != nil {
		return e
	}
	// Render against the proposed closure (not the previous selection), so watch
	// namespaces and controller-owned RBAC agree on the same desired state.
	cp := *p
	caps := *p.Capabilities
	caps.Active = names
	cp.Capabilities = &caps
	p = &cp
	missing, e := p.missingCapabilitySecrets(names)
	if e != nil {
		return e
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing sealed credentials: %v", missing)
	}
	rendered, e := p.RenderCapabilities(ctx)
	if e != nil {
		return e
	}
	if e = p.Write(rendered); e != nil {
		return e
	}
	if _, e = p.CheckCapabilities(ctx); e != nil {
		return e
	}
	files, e := p.CapabilityActivation(names)
	if e != nil {
		return e
	}
	b, _ := json.MarshalIndent(EnabledCapabilities{1, requested}, "", "  ")
	files[capabilityDir+"/enabled.json"] = append(b, '\n')
	return p.Write(files)
}
