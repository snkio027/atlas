package platform

import (
	"bytes"
	"strings"
	"testing"
)

func TestStrimziSupportedImageCatalog(t *testing.T) {
	p := candidate(t)
	versions := []string{"4.2.0", "4.2.1", "4.2.2", "4.3.0", "4.3.1"}
	entries := make([]string, 0, len(versions))
	for _, version := range versions {
		entries = append(entries, version+"=quay.io/strimzi/kafka:1.3.0-kafka-"+version)
	}
	upstream := strings.Join(entries, "\n")
	pinned, err := p.pinStrimziImages(upstream)
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range strings.Fields(pinned) {
		version, image, _ := strings.Cut(entry, "=")
		source := "quay.io/strimzi/kafka:1.3.0-kafka-" + versions[i]
		if version != versions[i] || image != p.Capabilities.Lock.Images[source] {
			t.Fatal("supported version lost or aliased to another Kafka binary", entry)
		}
	}
	if again, err := p.pinStrimziImages(pinned); err != nil || again != pinned {
		t.Fatal("pinned catalog is not idempotent", err)
	}
	for name, invalid := range map[string]string{
		"runtime-only regression":   entries[4],
		"missing supported version": strings.Join(entries[1:], "\n"),
		"duplicate version":         strings.Replace(upstream, entries[0], entries[1], 1),
		"unknown version":           strings.ReplaceAll(upstream, "4.2.0", "4.1.2"),
		"wrong binary for version":  strings.Replace(upstream, "kafka-4.2.0", "kafka-4.3.1", 1),
		"different digest":          strings.Replace(pinned, "@sha256:", "@sha256:f", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.pinStrimziImages(invalid); err == nil {
				t.Fatal("invalid supported-version catalog accepted")
			}
		})
	}
	delete(p.Capabilities.Lock.Images, "quay.io/strimzi/kafka:1.3.0-kafka-4.2.0")
	if _, err := p.pinStrimziImages(upstream); err == nil {
		t.Fatal("missing supply-chain pin accepted")
	}
}

func TestRenderedStrimziCatalogRetainsEverySupportedVersion(t *testing.T) {
	p := candidate(t)
	objects, err := p.capabilityObjects("kafka-operator")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range objects {
		if o["kind"] != "Deployment" {
			continue
		}
		for _, container := range slice(field(o, "spec", "template", "spec", "containers")) {
			for _, item := range slice(field(mapping(container), "env")) {
				env := mapping(item)
				switch name := str(env["name"]); name {
				case "STRIMZI_KAFKA_IMAGES", "STRIMZI_KAFKA_CONNECT_IMAGES", "STRIMZI_KAFKA_MIRROR_MAKER_2_IMAGES":
					value := str(env["value"])
					if want, err := p.pinStrimziImages(value); err != nil || value != want {
						t.Fatal("rendered operator cannot load supported image catalog", name, err)
					}
					seen[name] = true
				}
			}
		}
	}
	if len(seen) != 3 {
		t.Fatal("expected all three upstream supported-version maps", seen)
	}
}

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
