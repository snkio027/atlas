package workloadrun

import (
	"atlas-refactor/internal/installation"
	"atlas-refactor/internal/workload"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Key struct {
	Access string `json:"accessKey"`
	Secret string `json:"secretKey"`
}

func freshKey() (Key, error) {
	var k Key
	for _, s := range []*string{&k.Access, &k.Secret} {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			return k, e
		}
		*s = base64.RawURLEncoding.EncodeToString(b)
	}
	return k, nil
}

// ProviderPolicy grants only object operations and bucket listing. Legacy
// SeaweedFS Write:bucket also grants bucket configuration, so it is never used
// for a new Binding. Policy-only identities avoid the native-action union.
func ProviderPolicy(bucket string) Object {
	return Object{"Version": "2012-10-17", "Statement": []any{
		Object{"Effect": "Allow", "Action": []string{"s3:ListBucket", "s3:ListBucketMultipartUploads"}, "Resource": []string{"arn:aws:s3:::" + bucket}},
		Object{"Effect": "Allow", "Action": []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:GetObjectTagging", "s3:PutObjectTagging", "s3:DeleteObjectTagging"}, "Resource": []string{"arn:aws:s3:::" + bucket + "/*"}},
	}}
}
func ExtendProvider(base Object, bindings []workload.Binding, keys map[string]Key) (Object, error) {
	var p Object
	if e := json.Unmarshal(workload.JSON(base), &p); e != nil {
		return nil, e
	}
	identities := array(p["identities"])
	if len(identities) == 0 {
		return nil, errors.New("existing provider identity inventory is required")
	}
	policies := array(p["policies"])
	if policies == nil {
		policies = []any{}
	}
	for _, b := range bindings {
		k, ok := keys[b.ID()]
		if !ok || len(k.Access) < 32 || len(k.Secret) < 32 {
			return nil, errors.New("missing independent Binding key")
		}
		name := "atlas-" + b.ID()
		policyName := name + "-objects"
		want := Object{"name": name, "credentials": []any{Object{"accessKey": k.Access, "secretKey": k.Secret}}, "policyNames": []string{policyName}}
		found := false
		for _, raw := range identities {
			v := mapping(raw)
			if v["name"] == name {
				found = true
				if !bytes.Equal(workload.JSON(v), workload.JSON(want)) {
					return nil, errors.New("existing Binding identity differs")
				}
			} else {
				for _, c := range array(v["credentials"]) {
					if at(mapping(c), "accessKey") == k.Access {
						return nil, errors.New("credential reused by another identity")
					}
				}
			}
		}
		if !found {
			identities = append(identities, want)
		}
		policy := Object{"name": policyName, "content": string(workload.JSON(ProviderPolicy(b.Bucket)))}
		found = false
		for _, raw := range policies {
			v := mapping(raw)
			if v["name"] == policyName {
				found = true
				if !bytes.Equal(workload.JSON(v), workload.JSON(policy)) {
					return nil, errors.New("existing policy differs")
				}
			}
		}
		if !found {
			policies = append(policies, policy)
		}
	}
	p["identities"] = identities
	p["policies"] = policies
	return p, nil
}
func (w *Workflow) PrepareCredentials(ctx context.Context, p Plan, approval string) error {
	if e := w.approve(p, approval); e != nil {
		return e
	}
	if e := w.bind(ctx); e != nil {
		return e
	}
	current, e := w.remote(ctx)
	if e != nil || current != p.Parent {
		return errors.New("credential preparation requires the exact approved parent")
	}
	certPath := filepath.Join(w.Install.Config.StateDirectory, "sealing-certificate.pem")
	cert, e := regular(certPath, true)
	if e != nil {
		return e
	}
	if workload.Digest(cert) != p.CertificateSHA256 {
		return errors.New("sealing certificate changed")
	}
	receipt, e := regular(filepath.Join(w.Install.Config.BackupDirectory, w.Install.Record.InstallID, "receipt.json"), true)
	if e != nil {
		return e
	}
	var proof Object
	if e = json.Unmarshal(receipt, &proof); e != nil {
		return e
	}
	if proof["installID"] != p.InstallID || proof["clusterUID"] != p.ClusterUID || proof["certificateSHA256"] != p.CertificateSHA256 || proof["roundTrip"] != "PASS" {
		return errors.New("existing Trust Root backup receipt differs")
	}
	// Fetch only the public certificate; no private-key export/rotation/recovery.
	if e = installation.VerifyTools(w.Install.Config.StateDirectory, w.Install.Product.Tools); e != nil {
		return e
	}
	seal := filepath.Join(installation.ToolDirectory(w.Install.Config.StateDirectory, w.Install.Product.Tools), "kubeseal")
	currentCert, e := command(ctx, w.Config.StateDirectory, nil, seal, "--fetch-cert", "--controller-namespace", "sealed-secrets", "--controller-name", "sealed-secrets", "--kubeconfig", filepath.Join(w.Install.Config.StateDirectory, "runtime/.state/kubeconfig"), "--context", "kind-"+p.Cluster)
	if e != nil {
		return e
	}
	if workload.Digest(currentCert) != p.CertificateSHA256 {
		return errors.New("live controller certificate differs")
	}
	secret, e := w.get(ctx, "secret", "object-storage", "seaweedfs-auth")
	if e != nil {
		return e
	}
	raw, e := base64.StdEncoding.DecodeString(str(at(secret, "data", "seaweedfs_s3_config")))
	if e != nil {
		return e
	}
	var provider Object
	if e = installation.Decode(raw, &provider); e != nil {
		return e
	}
	legacyBytes, e := regular(filepath.Join(w.Install.Config.StateDirectory, "credentials.json"), true)
	if e != nil {
		return e
	}
	if workload.Digest(legacyBytes) != w.Install.Record.CredentialsSHA256 {
		return errors.New("D1 credential record changed")
	}
	var legacy installation.Credentials
	if e = installation.Decode(legacyBytes, &legacy); e != nil {
		return e
	}
	expected := Object{"identities": []any{Object{"name": "web-uploads", "credentials": []any{Object{"accessKey": legacy.AccessKey, "secretKey": legacy.SecretKey}}, "actions": []string{"Read:uploads", "Write:uploads", "List:uploads", "Tagging:uploads"}}}}
	if b, e := regular(filepath.Join(w.Config.StateDirectory, "provider.json"), true); e == nil {
		if e = installation.Decode(b, &expected); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if !bytes.Equal(workload.JSON(provider), workload.JSON(expected)) {
		return errors.New("live provider auth differs from the verified predecessor; preserve state")
	}
	keys := map[string]Key{}
	keyPath := filepath.Join(w.Config.StateDirectory, "keys.json")
	if b, e := regular(keyPath, true); e == nil {
		if e = workload.StrictDecode(b, &keys); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	for _, b := range w.Model.Intent.Bindings {
		if _, ok := keys[b.ID()]; !ok {
			k, e := freshKey()
			if e != nil {
				return e
			}
			keys[b.ID()] = k
		}
	}
	if e = save(keyPath, workload.JSON(keys), false); e != nil {
		return e
	}
	next, e := ExtendProvider(provider, w.Model.Intent.Bindings, keys)
	if e != nil {
		return e
	}
	a := &workload.Artifacts{Schema: 1, InstallID: p.InstallID, CertificateSHA256: p.CertificateSHA256, IntentSHA256: p.IntentSHA256, Clients: map[string]Object{}}
	if old, e := regular(filepath.Join(w.Config.StateDirectory, "artifacts.json"), true); e == nil {
		a, e = workload.DecodeArtifacts(old)
		if e != nil {
			return e
		}
		if a.InstallID != p.InstallID || a.CertificateSHA256 != p.CertificateSHA256 {
			return errors.New("old sealed registration differs")
		}
		a.IntentSHA256 = p.IntentSHA256
	} else if !os.IsNotExist(e) {
		return e
	}
	sealSecret := func(ns, name string, data map[string]string) (Object, error) {
		s := Object{"apiVersion": "v1", "kind": "Secret", "metadata": Object{"namespace": ns, "name": name}, "type": "Opaque", "stringData": data}
		b, e := command(ctx, w.Config.StateDirectory, workload.JSON(s), seal, "--cert", certPath, "--scope", "strict", "--format", "json")
		if e != nil {
			return nil, e
		}
		var out Object
		if e = json.Unmarshal(b, &out); e != nil {
			return nil, e
		}
		if at(out, "metadata", "namespace") != ns || at(out, "metadata", "name") != name {
			return nil, errors.New("sealed identity mismatch")
		}
		return Object{"apiVersion": "bitnami.com/v1alpha1", "kind": "SealedSecret", "metadata": Object{"namespace": ns, "name": name, "annotations": Object{"argocd.argoproj.io/sync-options": "Prune=confirm,Delete=false"}}, "spec": Object{"encryptedData": at(out, "spec", "encryptedData"), "template": Object{"metadata": Object{"namespace": ns, "name": name}, "type": "Opaque"}}}, nil
	}
	previous, e := regular(filepath.Join(w.Config.StateDirectory, "provider-prepared.json"), true)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if a.Provider == nil || !bytes.Equal(previous, workload.JSON(next)) {
		a.Provider, e = sealSecret("object-storage", "seaweedfs-auth", map[string]string{"seaweedfs_s3_config": string(workload.JSON(next))})
		if e != nil {
			return e
		}
	}
	for _, b := range w.Model.Intent.Bindings {
		if a.Clients[b.ID()] == nil {
			k := keys[b.ID()]
			a.Clients[b.ID()], e = sealSecret(b.Project, b.Secret(), map[string]string{"AWS_ACCESS_KEY_ID": k.Access, "AWS_SECRET_ACCESS_KEY": k.Secret})
			if e != nil {
				return e
			}
		}
	}
	if _, e = workload.Compile(w.Context, w.Model, "consumer", a); e != nil {
		return fmt.Errorf("prepared cipher rejected: %w", e)
	}
	if e = save(filepath.Join(w.Config.StateDirectory, "provider-prepared.json"), workload.JSON(next), false); e != nil {
		return e
	}
	if e = save(filepath.Join(w.Config.StateDirectory, "artifacts.json"), workload.JSON(a), false); e != nil {
		return e
	}
	return save(filepath.Join(w.Config.StateDirectory, "authority", approval, "credential-preparation.json"), workload.JSON(Object{"installID": p.InstallID, "clusterUID": p.ClusterUID, "certificateSHA256": p.CertificateSHA256, "intentSHA256": p.IntentSHA256, "artifactsSHA256": workload.Digest(workload.JSON(a)), "providerBeforeSHA256": workload.Digest(workload.JSON(provider)), "providerAfterSHA256": workload.Digest(workload.JSON(next)), "credentialsSHA256": workload.Digest(workload.JSON(keys)), "originalSecretUID": at(secret, "metadata", "uid")}), true)
}
