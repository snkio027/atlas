package platform

import (
	"bytes"
	"testing"
)

func TestDataCRDPrinterColumnSerialization(t *testing.T) {
	// Reproduce the API representation seen by Argo: an explicit priority:0
	// in the chart is absent from the live CRD. Nonzero priorities and schema
	// defaults are intentional input and must not be dropped with it.
	input := []byte(`{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":"tests.example.test"},"spec":{"versions":[{"name":"v1","additionalPrinterColumns":[{"name":"State","type":"string","jsonPath":".status.state","priority":0},{"name":"Detail","type":"string","jsonPath":".status.detail","priority":1},{"name":"Age","type":"date","jsonPath":".metadata.creationTimestamp"}],"schema":{"openAPIV3Schema":{"type":"object","properties":{"spec":{"type":"object","properties":{"priority":{"type":"integer","default":0}}}}}}},{"name":"v2","additionalPrinterColumns":[{"name":"State","type":"string","jsonPath":".status.state","priority":0}]}]}}`)
	for _, release := range []string{"cnpg", "strimzi", "flink-operator", "atlas-monitoring"} {
		t.Run(release, func(t *testing.T) {
			objects, err := decodeObjects(input)
			if err != nil {
				t.Fatal(err)
			}
			o := objects[0]
			original := encoded(objects)
			p := &Project{}
			if err := p.hardenDataOperator(release, o); err != nil {
				t.Fatal(err)
			}
			if release == "atlas-monitoring" {
				if !bytes.Equal(original, encoded(objects)) {
					t.Fatal("existing platform projection changed")
				}
				return
			}
			versions := slice(field(o, "spec", "versions"))
			for _, v := range versions {
				columns := slice(field(mapping(v), "additionalPrinterColumns"))
				if _, exists := mapping(columns[0])["priority"]; exists {
					t.Fatal("zero priority will differ from the API representation")
				}
			}
			columns := slice(field(mapping(versions[0]), "additionalPrinterColumns"))
			if field(mapping(columns[1]), "priority") != float64(1) || field(mapping(columns[2]), "priority") != nil {
				t.Fatal("nonzero or absent priority changed")
			}
			if field(mapping(versions[0]), "schema", "openAPIV3Schema", "properties", "spec", "properties", "priority", "default") != float64(0) {
				t.Fatal("CR schema default was changed")
			}
			first := encoded(objects)
			if err := p.hardenDataOperator(release, o); err != nil || !bytes.Equal(first, encoded(objects)) {
				t.Fatal("CRD projection is not idempotent", err)
			}
		})
	}
}

func TestRenderedDataCRDColumnsUseAPIRepresentation(t *testing.T) {
	p := candidate(t)
	for _, name := range []string{"postgresql-crds", "kafka-crds", "flink-crds"} {
		objects, err := p.capabilityObjects(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objects {
			for _, v := range slice(field(o, "spec", "versions")) {
				for _, c := range slice(field(mapping(v), "additionalPrinterColumns")) {
					if priority, ok := field(mapping(c), "priority").(float64); ok && priority == 0 {
						t.Fatalf("%s has an API-omitted zero priority", identity(o))
					}
				}
			}
		}
	}
}
