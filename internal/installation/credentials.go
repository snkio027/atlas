package installation

import (
	"atlas-refactor/internal/atlas"
	"atlas-refactor/internal/platformnames"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type Credentials struct {
	GrafanaPassword string `json:"grafanaPassword"`
	AccessKey       string `json:"accessKey"`
	SecretKey       string `json:"secretKey"`
}
type SealedRecord struct {
	CertificateSHA256 string           `json:"certificateSHA256"`
	CredentialsSHA256 string           `json:"credentialsSHA256"`
	Objects           []map[string]any `json:"objects"`
}

func randomCredential() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), e
}
func (w *Workflow) kube(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	path := filepath.Join(w.runtimeDir(), ".state/kubeconfig")
	b, e := privateRead(path)
	if e != nil {
		return nil, e
	}
	h, e := privateRead(path + ".sha256")
	if e != nil || Digest(b) != string(bytes.TrimSpace(h)) {
		return nil, errors.New("dedicated kubeconfig binding changed")
	}
	r := atlas.ExecRunner{Root: w.runtimeDir(), ToolDir: ToolDirectory(w.Config.StateDirectory, w.Product.Tools), DockerContext: "orbstack"}
	return r.Run(ctx, atlas.Request{Tool: "kubectl", Input: input, Args: append([]string{"--kubeconfig", path, "--context", "kind-" + w.Config.Cluster, "--request-timeout=30s"}, args...)})
}
func (w *Workflow) bindCluster(ctx context.Context) error {
	b, e := w.kube(ctx, nil, "get", "namespace", "kube-system", "-o", "json")
	if e != nil {
		return e
	}
	var obj struct {
		Kind     string
		Metadata struct{ Name, UID string }
	}
	if e = decodeLive(b, &obj); e != nil {
		return e
	}
	if obj.Kind != "Namespace" || obj.Metadata.Name != "kube-system" || obj.Metadata.UID == "" {
		return errors.New("invalid cluster identity")
	}
	if w.Record.ClusterUID != "" && w.Record.ClusterUID != obj.Metadata.UID {
		return errors.New("cluster UID changed")
	}
	if w.Record.ClusterUID == "" {
		w.Record.ClusterUID = obj.Metadata.UID
		return w.saveRecord()
	}
	return nil
}

// Live API representations contain server fields; configuration remains strict.
func decodeLive(b []byte, v any) error { return json.Unmarshal(b, v) }
func keyPair(secret []byte) ([]byte, []byte, error) {
	var obj struct {
		Kind, Type string
		Data       map[string]string
	}
	if e := decodeLive(secret, &obj); e != nil {
		return nil, nil, e
	}
	if obj.Kind != "Secret" || obj.Type != "kubernetes.io/tls" {
		return nil, nil, errors.New("invalid controller key secret")
	}
	cert, e := base64.StdEncoding.DecodeString(obj.Data["tls.crt"])
	if e != nil {
		return nil, nil, e
	}
	key, e := base64.StdEncoding.DecodeString(obj.Data["tls.key"])
	if e != nil {
		return nil, nil, e
	}
	block, rest := pem.Decode(cert)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("expected one certificate")
	}
	parsed, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, nil, e
	}
	pub, ok := parsed.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() < 4096 || time.Now().Before(parsed.NotBefore) || !time.Now().Before(parsed.NotAfter) {
		return nil, nil, errors.New("weak or expired controller certificate")
	}
	block, rest = pem.Decode(key)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("invalid controller private key")
	}
	var private *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		private, e = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else if block.Type == "PRIVATE KEY" {
		var k any
		k, e = x509.ParsePKCS8PrivateKey(block.Bytes)
		private, _ = k.(*rsa.PrivateKey)
	} else {
		return nil, nil, errors.New("unsupported controller private key")
	}
	if e != nil || private == nil || private.Validate() != nil || !private.PublicKey.Equal(pub) {
		return nil, nil, errors.New("backup private key does not match certificate")
	}
	return cert, key, nil
}
func (w *Workflow) seal(ctx context.Context, certPath string, secret map[string]any) (map[string]any, error) {
	b, e := command(ctx, w.Config.StateDirectory, JSON(secret), filepath.Join(ToolDirectory(w.Config.StateDirectory, w.Product.Tools), "kubeseal"), "--cert", certPath, "--scope", "strict", "--format", "json")
	if e != nil {
		return nil, e
	}
	var out map[string]any
	if e = Decode(b, &out); e != nil {
		return nil, e
	}
	meta, _ := out["metadata"].(map[string]any)
	want := secret["metadata"].(map[string]any)
	if out["kind"] != "SealedSecret" || out["apiVersion"] != "bitnami.com/v1alpha1" || meta["name"] != want["name"] || meta["namespace"] != want["namespace"] {
		return nil, errors.New("sealed object identity differs")
	}
	delete(out, "status")
	delete(meta, "creationTimestamp")
	meta["annotations"] = map[string]any{"argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}
	spec, _ := out["spec"].(map[string]any)
	encrypted, _ := spec["encryptedData"].(map[string]any)
	data := secret["stringData"].(map[string]string)
	if len(encrypted) != len(data) {
		return nil, errors.New("sealed key inventory differs")
	}
	for k := range data {
		if s, ok := encrypted[k].(string); !ok || s == "" {
			return nil, errors.New("empty ciphertext")
		}
	}
	return out, nil
}
func (w *Workflow) backup(ctx context.Context) error {
	if e := w.checkBackupLocation(); e != nil {
		return e
	}
	if e := w.bindCluster(ctx); e != nil {
		return e
	}
	b, e := w.kube(ctx, nil, "get", "secrets", "-n", platformnames.Secrets, "-l", "sealedsecrets.bitnami.com/sealed-secrets-key=active", "-o", "json")
	if e != nil {
		return e
	}
	var list struct {
		Kind  string
		Items []json.RawMessage
	}
	if e = decodeLive(b, &list); e != nil {
		return e
	}
	if (list.Kind != "SecretList" && list.Kind != "List") || len(list.Items) != 1 {
		return errors.New("fresh installation requires exactly one active sealing key")
	}
	var secret map[string]any
	if e = decodeLive(list.Items[0], &secret); e != nil {
		return e
	}
	// Backup only restore-relevant fields; resourceVersion/managedFields are
	// observations, not key identity. The controller key itself remains untouched.
	meta := secret["metadata"].(map[string]any)
	name, ok := meta["name"].(string)
	if !ok || name == "" || meta["namespace"] != platformnames.Secrets {
		return errors.New("unexpected key namespace/name")
	}
	keyObject := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": secret["type"], "metadata": map[string]any{"name": name, "namespace": platformnames.Secrets, "labels": meta["labels"]}, "data": secret["data"]}
	raw := JSON(keyObject)
	cert, _, e := keyPair(raw)
	if e != nil {
		return e
	}
	certDigest := Digest(cert)
	if w.Record.CertificateSHA256 != "" && w.Record.CertificateSHA256 != certDigest {
		return errors.New("instance sealing certificate changed")
	}
	dir := filepath.Join(w.Config.BackupDirectory, w.Record.InstallID)
	if e = save(filepath.Join(dir, "controller-key.json"), raw, true); e != nil {
		return e
	}
	readback, e := privateRead(filepath.Join(dir, "controller-key.json"))
	if e != nil || !bytes.Equal(readback, raw) {
		return errors.New("backup readback failed")
	}
	if _, _, e = keyPair(readback); e != nil {
		return e
	}
	certPath := filepath.Join(w.Config.StateDirectory, "sealing-certificate.pem")
	if e = save(certPath, cert, true); e != nil {
		return e
	}
	probeValue, e := randomCredential()
	if e != nil {
		return e
	}
	probe := secretObject(platformnames.Secrets, "atlas-backup-proof", map[string]string{"proof": probeValue})
	sealed, e := w.seal(ctx, certPath, probe)
	if e != nil {
		return e
	}
	plain, e := command(ctx, w.Config.StateDirectory, JSON(sealed), filepath.Join(ToolDirectory(w.Config.StateDirectory, w.Product.Tools), "kubeseal"), "--recovery-unseal", "--recovery-private-key", filepath.Join(dir, "controller-key.json"), "--format", "json")
	if e != nil {
		return e
	}
	var result struct {
		Kind string
		Data map[string]string
	}
	if e = decodeLive(plain, &result); e != nil {
		return e
	}
	if result.Kind != "Secret" || result.Data["proof"] != base64.StdEncoding.EncodeToString([]byte(probeValue)) {
		return errors.New("backup failed seal/unseal round trip")
	}
	proof := map[string]any{"installID": w.Record.InstallID, "clusterUID": w.Record.ClusterUID, "certificateSHA256": certDigest, "backupSHA256": Digest(raw), "backupPath": filepath.Join(dir, "controller-key.json"), "declaredIsolation": w.Config.BackupIsolation, "roundTrip": "PASS"}
	if e = save(filepath.Join(dir, "receipt.json"), JSON(proof), true); e != nil {
		return e
	}
	if e = save(filepath.Join(w.Config.StateDirectory, "authority", "trust-root.json"), JSON(proof), true); e != nil {
		return e
	}
	w.Record.CertificateSHA256 = certDigest
	return w.saveRecord()
}
func secretObject(ns, name string, data map[string]string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"namespace": ns, "name": name}, "type": "Opaque", "stringData": data}
}
func (w *Workflow) credentials(ctx context.Context) ([]byte, error) {
	if !sha.MatchString(w.Record.CertificateSHA256) {
		return nil, errors.New("verified backup is required before credentials")
	}
	path := filepath.Join(w.Config.StateDirectory, "credentials.json")
	b, e := privateRead(path)
	var c Credentials
	if os.IsNotExist(e) {
		if c.GrafanaPassword, e = randomCredential(); e != nil {
			return nil, e
		}
		if c.AccessKey, e = randomCredential(); e != nil {
			return nil, e
		}
		if c.SecretKey, e = randomCredential(); e != nil {
			return nil, e
		}
		b = JSON(c)
		if e = save(path, b, true); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	if e = Decode(b, &c); e != nil {
		return nil, e
	}
	if len(c.GrafanaPassword) < 32 || len(c.AccessKey) < 32 || len(c.SecretKey) < 32 {
		return nil, errors.New("invalid instance credentials")
	}
	credentialHash := Digest(b)
	recordPath := filepath.Join(w.Config.StateDirectory, "sealed.json")
	old, e := privateRead(recordPath)
	var sealed SealedRecord
	if e == nil {
		if e = Decode(old, &sealed); e != nil {
			return nil, e
		}
		if sealed.CertificateSHA256 != w.Record.CertificateSHA256 || sealed.CredentialsSHA256 != credentialHash || len(sealed.Objects) != 3 {
			return nil, errors.New("saved sealed credentials differ from instance binding")
		}
	} else if os.IsNotExist(e) {
		s3 := JSON(map[string]any{"identities": []any{map[string]any{"name": "web-uploads", "credentials": []any{map[string]string{"accessKey": c.AccessKey, "secretKey": c.SecretKey}}, "actions": []string{"Read:uploads", "Write:uploads", "List:uploads", "Tagging:uploads"}}}})
		secrets := []map[string]any{secretObject(platformnames.Monitoring, "grafana-admin", map[string]string{"admin-user": "admin", "admin-password": c.GrafanaPassword}), secretObject(platformnames.ObjectStorage, "seaweedfs-auth", map[string]string{"seaweedfs_s3_config": string(s3)}), secretObject(platformnames.Smoke, "s3-client", map[string]string{"AWS_ACCESS_KEY_ID": c.AccessKey, "AWS_SECRET_ACCESS_KEY": c.SecretKey})}
		sealed = SealedRecord{CertificateSHA256: w.Record.CertificateSHA256, CredentialsSHA256: credentialHash}
		for _, secret := range secrets {
			o, e := w.seal(ctx, filepath.Join(w.Config.StateDirectory, "sealing-certificate.pem"), secret)
			if e != nil {
				return nil, e
			}
			sealed.Objects = append(sealed.Objects, o)
		}
		if e = save(recordPath, JSON(sealed), true); e != nil {
			return nil, e
		}
	} else {
		return nil, e
	}
	out := JSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": sealed.Objects})
	if w.Record.CredentialsSHA256 != "" && w.Record.CredentialsSHA256 != credentialHash || w.Record.SealedSHA256 != "" && w.Record.SealedSHA256 != Digest(out) {
		return nil, errors.New("recorded credentials or ciphertext changed")
	}
	w.Record.CredentialsSHA256 = credentialHash
	w.Record.SealedSHA256 = Digest(out)
	return out, w.saveRecord()
}
