package platform

import (
	"atlas-refactor/internal/platformnames"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// The reviewed input uses integer cores/millicores and binary memory/storage
// units. Reject a new representation rather than silently rounding a budget.
func layoutQuantity(t *testing.T, key string, value any) int64 {
	t.Helper()
	s := fmt.Sprint(value)
	multiplier := int64(1)
	if strings.HasSuffix(s, "Gi") {
		s, multiplier = strings.TrimSuffix(s, "Gi"), 1024*1024*1024
	} else if strings.HasSuffix(s, "Mi") {
		s, multiplier = strings.TrimSuffix(s, "Mi"), 1024*1024
	} else if strings.HasSuffix(key, "cpu") {
		if strings.HasSuffix(s, "m") {
			s = strings.TrimSuffix(s, "m")
		} else {
			multiplier = 1000
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		t.Fatalf("unsupported budget %s=%v", key, value)
	}
	return n * multiplier
}

func TestOperatorSplitConservesBudgetAndFitsRollout(t *testing.T) {
	p := candidate(t)
	files, err := p.BundleFiles()
	if err != nil {
		t.Fatal(err)
	}
	all := []Object{}
	for name, b := range files {
		if !strings.HasPrefix(name, "gitops/") || strings.HasSuffix(name, "kustomization.yaml") {
			continue
		}
		xs, err := DecodeJSONManifests(b)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, xs...)
	}
	quotas := map[string]Object{}
	for _, o := range all {
		if o["kind"] == "ResourceQuota" {
			quotas[str(metadata(o)["namespace"])] = mapping(field(o, "spec", "hard"))
		}
	}
	for _, pair := range [][2]string{{platformnames.CNPGOperator, platformnames.PostgreSQL}, {platformnames.StrimziOperator, platformnames.Kafka}, {platformnames.FlinkOperator, platformnames.Flink}} {
		operator, service := quotas[pair[0]], quotas[pair[1]]
		if operator == nil || service == nil {
			t.Fatal("missing split quota", pair)
		}
		for key, total := range map[string]string{"requests.cpu": "3", "requests.memory": "6Gi", "limits.memory": "8Gi", "requests.storage": "32Gi", "persistentvolumeclaims": "8", "pods": "16"} {
			if layoutQuantity(t, key, operator[key])+layoutQuantity(t, key, service[key]) != layoutQuantity(t, key, total) {
				t.Fatal("split changed total", pair, key)
			}
		}
		peak := map[string]int64{}
		for _, o := range all {
			if o["kind"] != "Deployment" || metadata(o)["namespace"] != pair[0] {
				continue
			}
			if field(o, "spec", "replicas") != float64(1) {
				t.Fatal("re-review operator rollout budget", identity(o))
			}
			pods := int64(2)
			if field(o, "spec", "strategy", "type") == "Recreate" {
				pods = 1
			}
			peak["pods"] += pods
			for _, raw := range slice(field(o, "spec", "template", "spec", "containers")) {
				c := mapping(raw)
				for _, key := range []string{"requests.cpu", "requests.memory", "limits.memory"} {
					parts := strings.Split(key, ".")
					peak[key] += pods * layoutQuantity(t, key, field(c, "resources", parts[0], parts[1]))
				}
			}
		}
		if peak["pods"] == 0 {
			t.Fatal("operator deployment missing", pair[0])
		}
		for key, value := range peak {
			if value > layoutQuantity(t, key, operator[key]) {
				t.Fatal("operator rollout exceeds quota", pair[0], key)
			}
		}
	}
}
