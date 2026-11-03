package platform

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ADR-0026 reviews a finite finalizer PATCH increment. A future chart must not
// silently widen the kinds or verbs covered by this increment.
func TestMonitoringOperatorFinalizerPermissions(t *testing.T) {
	objects, err := candidate(t).capabilityObjects("monitoring-operator")
	if err != nil {
		t.Fatal(err)
	}
	grants := []string{}
	roles := 0
	for _, object := range objects {
		if object["kind"] != "ClusterRole" || metadata(object)["name"] != "atlas-monitoring-operator" {
			continue
		}
		roles++
		for _, value := range slice(object["rules"]) {
			rule := mapping(value)
			for _, resource := range slice(rule["resources"]) {
				if !strings.HasSuffix(str(resource), "/finalizers") {
					continue
				}
				if !reflect.DeepEqual(rule["apiGroups"], []any{"monitoring.coreos.com"}) {
					// The pre-existing services/finalizers grant is outside this increment.
					if reflect.DeepEqual(rule["apiGroups"], []any{""}) && resource == "services/finalizers" {
						continue
					}
					t.Fatal("unreviewed finalizer API group", rule)
				}
				for _, verb := range slice(rule["verbs"]) {
					grants = append(grants, str(resource)+":"+str(verb))
				}
			}
		}
	}
	want := []string{}
	for _, resource := range []string{"alertmanagers", "prometheusagents", "prometheuses", "thanosrulers"} {
		for _, verb := range []string{"patch", "update"} {
			want = append(want, resource+"/finalizers:"+verb)
		}
	}
	sort.Strings(grants)
	sort.Strings(want)
	if roles != 1 || !reflect.DeepEqual(grants, want) {
		t.Fatalf("monitoring finalizer grants differ from ADR-0026: roles=%d grants=%v", roles, grants)
	}
}
