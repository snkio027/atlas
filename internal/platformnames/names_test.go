package platformnames

import "testing"

func TestProductNamespaceSetRejectsUnknownAndIncompleteLayout(t *testing.T) {
	names := []string{}
	for _, n := range Definitions() {
		names = append(names, n.Name)
	}
	if err := ValidateDefinitions(names); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]string{append(append([]string{}, names...), "service-unregistered"), append(append([]string{}, names...), "argocd"), append(append([]string{}, names...), Argo), names[1:]} {
		if ValidateDefinitions(invalid) == nil {
			t.Fatal("invalid product namespace set accepted", invalid)
		}
	}
}

func TestProjectNamesAreDistinctAndReservedNamesDoNotAlias(t *testing.T) {
	if Project("archive") == Project("archive-dev") || Project("archive") != "project-archive" {
		t.Fatal("project identity collision")
	}
	for _, id := range []string{"platform-smoke", "project-platform-smoke", "platform-gateway", "gateway", "service-kafka", "kafka", "project-archive"} {
		if !Reserved(id) {
			t.Fatal("namespace prefix bypass", id)
		}
	}
}
