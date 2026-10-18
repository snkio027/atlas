package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfflineCredentialPreparationIsPrivateScopedAndRepeatable(t *testing.T) {
	tool := os.Getenv("ATLAS_TEST_KUBESEAL")
	if tool == "" {
		t.Skip("set ATLAS_TEST_KUBESEAL to locked kubeseal for offline encryption test")
	}
	p := candidate(t)
	sourceRoot := p.Root
	p.Root = t.TempDir()
	dir := filepath.Join(p.Root, capabilityDir, "resources")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for _, rel := range []string{"kubeseal.lock.json"} {
		b, e := readInput(filepath.Join(sourceRoot, capabilityDir, rel))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(p.Root, capabilityDir, rel), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(dir, "platform-credentials.json"), []byte(`{"apiVersion":"v1","kind":"List","items":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	// Disposable in-memory test key only; never installed as a cluster trust root.
	key, e := rsa.GenerateKey(rand.Reader, 4096)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "offline-fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageKeyEncipherment}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	cert := filepath.Join(p.Root, "test-public.pem")
	if e = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = p.PrepareCredentials(context.Background(), cert, tool); e != nil {
		t.Fatal(e)
	}
	privatePath := filepath.Join(p.Root, ".state/capabilities/credentials.json")
	st, e := os.Stat(privatePath)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("private permissions", e)
	}
	var credentials localCredentials
	if e = readJSON(privatePath, &credentials); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(p.Root, capabilityDir, "resources/platform-credentials.json")
	before, e := readInput(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{credentials.GrafanaPassword, credentials.AccessKey, credentials.SecretKey} {
		if bytes.Contains(before, []byte(secret)) {
			t.Fatal("plaintext leaked")
		}
	}
	missing, e := p.missingCapabilitySecrets([]string{"monitoring", "object-storage"})
	if e != nil || len(missing) > 0 {
		t.Fatal(missing, e)
	}
	if e = p.PrepareCredentials(context.Background(), cert, tool); e != nil {
		t.Fatal(e)
	}
	after, _ := readInput(path)
	if !bytes.Equal(before, after) {
		t.Fatal("repeat preparation rotated ciphertext")
	}
	objects, e := decodeObjects(after)
	if e != nil || len(objects) != 3 {
		t.Fatal(len(objects), e)
	}
	for _, o := range objects {
		data := mapping(field(o, "spec", "encryptedData"))
		if len(data) == 0 {
			t.Fatal("missing ciphertext")
		}
		if _, e := json.Marshal(o); e != nil {
			t.Fatal(e)
		}
	}
	// The second profile must neither rewrite D1 ciphertext nor reuse its secrets.
	dataPath := filepath.Join(dir, "data-credentials.json")
	if e = os.WriteFile(dataPath, []byte(`{"apiVersion":"v1","kind":"List","items":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = p.PrepareCredentialProfile(context.Background(), cert, tool, "data-services"); e != nil {
		t.Fatal(e)
	}
	dataBefore, e := readInput(dataPath)
	if e != nil {
		t.Fatal(e)
	}
	dataObjects, e := decodeObjects(dataBefore)
	if e != nil || len(dataObjects) != 3 {
		t.Fatal("expected independent PG/Redis/Flink sealed credentials", e)
	}
	for _, o := range dataObjects {
		if metadata(o)["name"] == "postgresql-app" && field(o, "spec", "template", "type") != "kubernetes.io/basic-auth" {
			t.Fatal("CNPG requires a basic-auth Secret")
		}
	}
	var dataCredentials dataServiceCredentials
	if e = readJSON(filepath.Join(p.Root, ".state/data-services/credentials.json"), &dataCredentials); e != nil {
		t.Fatal(e)
	}
	if dataCredentials.RedisPassword == credentials.AccessKey {
		t.Fatal("reused D1 credential")
	}
	for _, value := range []string{dataCredentials.PostgresPassword, dataCredentials.RedisPassword, dataCredentials.FlinkWebhookPassword} {
		if bytes.Contains(dataBefore, []byte(value)) {
			t.Fatal("data plaintext leaked")
		}
	}
	if e = p.PrepareCredentialProfile(context.Background(), cert, tool, "data-services"); e != nil {
		t.Fatal(e)
	}
	dataAfter, _ := readInput(dataPath)
	platformAfter, _ := readInput(path)
	if !bytes.Equal(dataBefore, dataAfter) || !bytes.Equal(platformAfter, before) {
		t.Fatal("credential preparation rewrote prior output")
	}
	if missing, e = p.missingCapabilitySecrets([]string{"postgresql", "redis", "flink-operator"}); e != nil || len(missing) != 0 {
		t.Fatal(missing, e)
	}

}
