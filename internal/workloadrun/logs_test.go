package workloadrun

import (
	"atlas-refactor/internal/workload"
	"strings"
	"testing"
)

func TestLogPodAcceptsKubernetesNullStatusWithoutRequiringReady(t *testing.T) {
	// kubectl's real List shape, including nullable API timestamps. Logs must
	// remain readable while diagnosing a Pending or unready workload too.
	raw := []byte(`{"apiVersion":"v1","kind":"List","items":[
		{"metadata":{"name":"web-b","namespace":"project-archive","uid":"second"},"spec":{"serviceAccountName":"web"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True","lastProbeTime":null}]}},
		{"metadata":{"name":"web-a","namespace":"project-archive","uid":"first"},"spec":{"serviceAccountName":"web"},"status":{"phase":"Pending","conditions":[{"type":"Ready","status":"False","lastProbeTime":null}]}}
	]}`)
	v := workload.Workload{Project: "archive", Name: "web"}
	pod, err := logPod(raw, v)
	if err != nil || at(pod, "metadata", "name") != "web-a" {
		t.Fatal(pod, err)
	}
	// The live-response fix must not weaken the authored-input parser.
	var authored Object
	if err := workload.StrictDecode(raw, &authored); err == nil || !strings.Contains(err.Error(), "null is forbidden") {
		t.Fatal("authored no-null contract changed", err)
	}
}

func TestLogPodRejectsMalformedResponseAndForeignIdentity(t *testing.T) {
	v := workload.Workload{Project: "archive", Name: "web"}
	valid := `{"items":[{"metadata":{"name":"web-a","namespace":"project-archive","uid":"first"},"spec":{"serviceAccountName":"web"}}]}`
	for name, raw := range map[string]string{
		"malformed":       `{"items":`,
		"trailing-value":  valid + `{}`,
		"no-pods":         `{"items":[]}`,
		"null-item":       `{"items":[null]}`,
		"wrong-namespace": strings.Replace(valid, `"project-archive"`, `"other"`, 1),
		"missing-uid":     strings.Replace(valid, `"first"`, `""`, 1),
		"wrong-account":   strings.Replace(valid, `"serviceAccountName":"web"`, `"serviceAccountName":"other"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := logPod([]byte(raw), v); err == nil {
				t.Fatal("invalid log target accepted")
			}
		})
	}
}
