package installation

import (
	"atlas-refactor/internal/platformcheck"
	"atlas-refactor/internal/platformnames"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

func (w *Workflow) Verify(ctx context.Context, functional bool) error {
	if !commit.MatchString(w.Record.FullCommit) || !sha.MatchString(w.Record.SealedSHA256) {
		return errors.New("full deployment has not been published")
	}
	if e := VerifyTools(w.Config.StateDirectory, w.Product.Tools); e != nil {
		return e
	}
	if e := w.checkGit(ctx); e != nil {
		return e
	}
	if e := w.bindCluster(ctx); e != nil {
		return e
	}
	if e := w.verifyAuthority(ctx); e != nil {
		return e
	}
	full, e := w.desiredFull()
	if e != nil {
		return e
	}
	if e = w.checkApplications(ctx, full); e != nil {
		return e
	}
	b, e := w.kube(ctx, nil, "get", "pods", "-A", "-o", "json")
	if e != nil {
		return e
	}
	var pods struct{ Items []platformcheck.Pod }
	if e = decodeLive(b, &pods); e != nil {
		return e
	}
	if e = platformcheck.VerifyPods(pods.Items, w.Config.Cluster, platformnames.Gateway); e != nil {
		var pending platformcheck.PendingPod
		if errors.As(e, &pending) {
			return pendingState(e.Error())
		}
		return e
	}
	b, e = w.kube(ctx, nil, "get", "pvc", "-A", "-o", "json")
	if e != nil {
		return e
	}
	var claims struct{ Items []map[string]any }
	if e = decodeLive(b, &claims); e != nil {
		return e
	}
	web, storage := false, false
	for _, pvc := range claims.Items {
		if nested(pvc, "spec", "storageClassName") != "atlas-local-retain" || nested(pvc, "status", "phase") != "Bound" {
			return errors.New("platform PVC is not Bound to retained local storage")
		}
		name, _ := nested(pvc, "spec", "volumeName").(string)
		if name == "" {
			return errors.New("PVC lacks PV binding")
		}
		b, e = w.kube(ctx, nil, "get", "pv", name, "-o", "json")
		if e != nil {
			return e
		}
		var pv map[string]any
		if e = decodeLive(b, &pv); e != nil {
			return e
		}
		if nested(pv, "spec", "persistentVolumeReclaimPolicy") != "Retain" || nested(pv, "spec", "claimRef", "uid") != nested(pvc, "metadata", "uid") {
			return errors.New("PV reclaim/claim identity mismatch")
		}
		web = web || nested(pvc, "metadata", "namespace") == "workload-web" && nested(pvc, "metadata", "name") == "web-data"
		storage = storage || nested(pvc, "metadata", "namespace") == "object-storage" && nested(pvc, "metadata", "name") == "seaweedfs-data"
	}
	if !web || !storage {
		return errors.New("required Web/S3 persistent storage absent")
	}
	b, e = w.kube(ctx, nil, "get", "secret", "development-ca", "-n", "gateway", "-o", `jsonpath={.data.tls\.crt}`)
	if e != nil {
		return e
	}
	ca, e := base64.StdEncoding.DecodeString(string(b))
	if e != nil {
		return e
	}
	if e = save(filepath.Join(w.Config.StateDirectory, "development-ca.crt"), ca, true); e != nil {
		return e
	}
	if e = w.verifyWeb(ctx); e != nil {
		return e
	}
	credentials, e := privateRead(filepath.Join(w.Config.StateDirectory, "credentials.json"))
	if e != nil {
		return e
	}
	if Digest(credentials) != w.Record.CredentialsSHA256 {
		return errors.New("credential digest changed")
	}
	var c Credentials
	if e = Decode(credentials, &c); e != nil {
		return e
	}
	if e = w.verifyMonitoring(ctx, c, functional); e != nil {
		return e
	}
	if e = w.verifyS3(ctx, c, functional); e != nil {
		return e
	}
	return save(filepath.Join(w.Config.StateDirectory, "latest-verification.json"), JSON(map[string]any{"result": "PASS", "at": time.Now().UTC().Format(time.RFC3339), "functionalWrites": functional, "applications": w.applications, "clusterUID": w.Record.ClusterUID, "deploymentCommit": w.Record.FullCommit, "checks": []string{"Bootstrap authority", "four Ready nodes", "exact GitOps Applications", "Pod placement", "PVC/PV retention", "Web HTTPS/redirect", "Grafana authentication/dashboard", "Prometheus targets/nodes/rules", "Alertmanager API", "S3 authorization"}}), false)
}
func (w *Workflow) verifyWeb(ctx context.Context) error {
	client, e := w.webClient()
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	for _, scheme := range []string{"http", "https"} {
		port := w.Config.HTTPPort
		if scheme == "https" {
			port = w.Config.HTTPSPort
		}
		req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s://web.atlas.test:%d/", scheme, port), nil)
		if e != nil {
			return e
		}
		res, e := client.Do(req)
		if e != nil {
			return e
		}
		body, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if e != nil {
			return e
		}
		if scheme == "http" {
			if res.StatusCode != 301 || res.Header.Get("Location") != fmt.Sprintf("https://web.atlas.test:%d/", w.Config.HTTPSPort) {
				return errors.New("Web redirect differs from approved TLS port")
			}
		} else if res.StatusCode != 200 || !bytes.HasPrefix(body, []byte("Atlas development web:")) {
			return errors.New("Web HTTPS content failed")
		}
	}
	return nil
}
func api(ctx context.Context, client *http.Client, method, address string, body []byte, user, password string) ([]byte, int, error) {
	req, e := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if e != nil {
		return nil, 0, e
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	res, e := client.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return b, res.StatusCode, e
}
func (w *Workflow) verifyMonitoring(ctx context.Context, c Credentials, functional bool) error {
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	grafana, closeG, _, e := w.forward(ctx, "grafana")
	if e != nil {
		return e
	}
	defer closeG()
	b, status, e := api(ctx, client, "GET", grafana+"/api/search?type=dash-db", nil, "admin", c.GrafanaPassword)
	if e != nil {
		return e
	}
	var dashboards []map[string]any
	if status != 200 || json.Unmarshal(b, &dashboards) != nil {
		return errors.New("Grafana authenticated dashboards unavailable")
	}
	if len(dashboards) == 0 {
		return pendingState("waiting for provisioned Grafana dashboards")
	}
	_, status, e = api(ctx, client, "GET", grafana+"/api/search", nil, "", "")
	if e != nil || status != 401 {
		return errors.New("Grafana anonymous access was not denied")
	}
	prometheus, closeP, _, e := w.forward(ctx, "prometheus")
	if e != nil {
		return e
	}
	defer closeP()
	b, status, e = api(ctx, client, "GET", prometheus+"/api/v1/targets", nil, "", "")
	if e != nil {
		return e
	}
	var targets map[string]any
	if status != 200 || json.Unmarshal(b, &targets) != nil || targets["status"] != "success" {
		return errors.New("Prometheus targets unavailable")
	}
	required := map[string]int{"seaweedfs": 1, "atlas-monitoring-kube-state-metrics": 1, "atlas-monitoring-prometheus-node-exporter": 4}
	seen := map[string]int{}
	for _, t := range array(nested(targets, "data", "activeTargets")) {
		m := mapping(t)
		if m["health"] != "up" {
			return pendingState("waiting for healthy Prometheus targets")
		}
		service, _ := nested(m, "labels", "service").(string)
		seen[service]++
	}
	for service, count := range required {
		if seen[service] != count {
			return pendingState(fmt.Sprintf("waiting for expected %s targets", service))
		}
	}
	b, status, e = api(ctx, client, "GET", prometheus+"/api/v1/query?query="+url.QueryEscape("kube_node_info"), nil, "", "")
	if e != nil {
		return e
	}
	var nodes map[string]any
	if status != 200 || json.Unmarshal(b, &nodes) != nil || nodes["status"] != "success" {
		return errors.New("node metrics query failed")
	}
	names := map[string]bool{}
	for _, item := range array(nested(nodes, "data", "result")) {
		name, _ := nested(mapping(item), "metric", "node").(string)
		names[name] = true
	}
	for _, suffix := range []string{"-control-plane", "-worker", "-worker2", "-worker3"} {
		if !names[w.Config.Cluster+suffix] {
			return pendingState("waiting for four-node metrics inventory")
		}
	}
	if len(names) != 4 {
		return errors.New("unexpected metrics node inventory")
	}
	b, status, e = api(ctx, client, "GET", prometheus+"/api/v1/rules", nil, "", "")
	if e != nil {
		return e
	}
	var rules map[string]any
	if status != 200 || json.Unmarshal(b, &rules) != nil || rules["status"] != "success" {
		return errors.New("Prometheus rules unavailable")
	}
	watchdog := false
	for _, g := range array(nested(rules, "data", "groups")) {
		for _, r := range array(nested(mapping(g), "rules")) {
			if nested(mapping(r), "name") == "Watchdog" && nested(mapping(r), "health") == "ok" {
				watchdog = true
			}
		}
	}
	if !watchdog {
		return pendingState("waiting for the healthy Watchdog alerting rule")
	}
	alert, closeA, _, e := w.forward(ctx, "alertmanager")
	if e != nil {
		return e
	}
	defer closeA()
	b, status, e = api(ctx, client, "GET", alert+"/api/v2/alerts", nil, "", "")
	if e != nil {
		return e
	}
	var alerts []map[string]any
	if status != 200 || json.Unmarshal(b, &alerts) != nil {
		return errors.New("Alertmanager API unavailable")
	}
	delivered := false
	for _, a := range alerts {
		delivered = delivered || nested(a, "labels", "alertname") == "Watchdog"
	}
	if !delivered {
		return pendingState("waiting for Prometheus Watchdog delivery to Alertmanager")
	}
	if functional {
		starts := time.Now().UTC().Add(-time.Minute)
		ends := time.Now().UTC().Add(2 * time.Minute)
		labels := map[string]string{"alertname": "AtlasD1Acceptance", "install_id": w.Record.InstallID}
		alertBody := func(end time.Time) []byte {
			return JSON([]any{map[string]any{"labels": labels, "startsAt": starts.Format(time.RFC3339), "endsAt": end.Format(time.RFC3339)}})
		}
		_, status, e = api(ctx, client, "POST", alert+"/api/v2/alerts", alertBody(ends), "", "")
		if e != nil || status != 200 {
			return errors.New("Alertmanager acceptance alert failed")
		}
		b, status, e = api(ctx, client, "GET", alert+"/api/v2/alerts?filter="+url.QueryEscape("install_id=\""+w.Record.InstallID+"\""), nil, "", "")
		if e != nil || status != 200 || json.Unmarshal(b, &alerts) != nil || len(alerts) != 1 {
			return errors.New("acceptance alert not observed")
		}
		_, status, e = api(ctx, client, "POST", alert+"/api/v2/alerts", alertBody(time.Now().UTC().Add(-time.Second)), "", "")
		if e != nil || status != 200 {
			return errors.New("acceptance alert resolution failed")
		}
	}
	return nil
}
