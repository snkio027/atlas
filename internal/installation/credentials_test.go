package installation

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 4096)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-test-only"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageKeyEncipherment}
	cert, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	return JSON(map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls", "metadata": map[string]string{"namespace": "sealed-secrets", "name": "synthetic-unit-test"}, "data": map[string]string{"tls.crt": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})), "tls.key": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}})
}
func TestBackupKeyPairAndRealSealingRoundTrip(t *testing.T) {
	p := testProduct(t)
	c := testConfig(t)
	w := Workflow{Config: c, Product: p}
	raw := testKey(t)
	cert, _, e := keyPair(raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = save(filepath.Join(c.BackupDirectory, "key.json"), raw, true); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(c.StateDirectory, "certificate.pem")
	if e = save(path, cert, true); e != nil {
		t.Fatal(e)
	}
	executable := os.Getenv("ATLAS_TEST_KUBESEAL")
	if executable == "" {
		t.Skip("set ATLAS_TEST_KUBESEAL for real sealed-secret roundtrip")
	}
	binary, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range p.Tools {
		if tool.Name == "kubeseal" && Digest(binary) != tool.BinarySHA256 {
			t.Fatal("unlocked test kubeseal")
		}
	}
	toolPath := filepath.Join(ToolDirectory(c.StateDirectory, p.Tools), "kubeseal")
	if e = save(toolPath, binary, true); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(toolPath, 0700); e != nil {
		t.Fatal(e)
	}
	sealed, e := w.seal(context.Background(), path, secretObject("workload-web", "probe", map[string]string{"proof": "synthetic-not-a-real-secret"}))
	if e != nil {
		t.Fatal(e)
	}
	plain, e := command(context.Background(), c.StateDirectory, JSON(sealed), toolPath, "--recovery-unseal", "--recovery-private-key", filepath.Join(c.BackupDirectory, "key.json"), "--format", "json")
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if e = Decode(plain, &out); e != nil {
		t.Fatal(e)
	}
	if nested(out, "data", "proof") != base64.StdEncoding.EncodeToString([]byte("synthetic-not-a-real-secret")) {
		t.Fatal("roundtrip mismatch")
	}
	// Strict namespace/name scope must remain cryptographically enforced.
	sealed["metadata"].(map[string]any)["namespace"] = "another-namespace"
	if _, e = command(context.Background(), c.StateDirectory, JSON(sealed), toolPath, "--recovery-unseal", "--recovery-private-key", filepath.Join(c.BackupDirectory, "key.json"), "--format", "json"); e == nil {
		t.Fatal("namespace rebinding decrypted")
	}
}
func TestCredentialPhaseRequiresVerifiedBackup(t *testing.T) {
	w := Workflow{Config: testConfig(t)}
	if _, e := w.credentials(context.Background()); e == nil {
		t.Fatal("credentials generated before backup")
	}
	if _, e := os.Stat(filepath.Join(w.Config.StateDirectory, "credentials.json")); !os.IsNotExist(e) {
		t.Fatal("premature private credential material")
	}
}
