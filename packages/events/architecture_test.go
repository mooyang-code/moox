package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestEventContractArchitecture(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, schema := range registry.Schemas() {
		if schema.Payload == "google.protobuf.Struct" {
			t.Fatalf("event %s still uses untyped Struct payload", schema.Name)
		}
		if _, ok := registry.PayloadFactory(schema.Payload); !ok {
			t.Fatalf("event %s payload %s has no factory", schema.Name, schema.Payload)
		}
		family, err := registry.FamilyPattern(EventType{Name: schema.Name, Version: schema.Version})
		if err != nil || family == "" {
			t.Fatalf("event %s family = %q, err = %v", schema.Name, family, err)
		}
	}

	descriptor := (&eventpb.EventMessage{}).ProtoReflect().Descriptor()
	for i := 0; i < descriptor.Fields().Len(); i++ {
		field := descriptor.Fields().Get(i)
		switch field.Name() {
		case "event_id", "event_name", "event_version", "space_id", "subject_id", "occurred_at", "payload":
		default:
			t.Fatalf("EventMessage contains out-of-contract field %q", field.Name())
		}
	}
	if descriptor.Fields().ByName(protoreflect.Name("producer")) != nil || descriptor.Fields().ByName(protoreflect.Name("protocol_version")) != nil {
		t.Fatal("EventMessage contains removed legacy envelope metadata")
	}
}
