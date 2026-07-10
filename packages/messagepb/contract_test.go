package messagepb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestMooxMessageContract(t *testing.T) {
	d := (&MooxMessage{}).ProtoReflect().Descriptor()
	want := map[protoreflect.Name]protoreflect.FieldNumber{
		"protocol_version": 1,
		"message_id":       2,
		"topic":            3,
		"kind":             4,
		"producer":         5,
		"space_id":         6,
		"sequence":         7,
		"occurred_at":      8,
		"published_at":     9,
		"content_type":     10,
		"payload":          11,
		"trace":            12,
		"attributes":       13,
	}
	for name, number := range want {
		field := d.Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Fatalf("field %s number mismatch: got %v", name, field)
		}
	}
	if got := d.Fields().Len(); got != len(want) {
		t.Fatalf("field count = %d, want %d", got, len(want))
	}
	for _, name := range []protoreflect.Name{
		"resource_key", "partition_key", "correlation_id", "causation_id",
	} {
		if d.Fields().ByName(name) != nil {
			t.Fatalf("forbidden field %q is present", name)
		}
	}

	wantKinds := map[string]MessageKind{
		"MESSAGE_KIND_UNSPECIFIED": MessageKind_MESSAGE_KIND_UNSPECIFIED,
		"MESSAGE_KIND_EVENT":       MessageKind_MESSAGE_KIND_EVENT,
		"MESSAGE_KIND_COMMAND":     MessageKind_MESSAGE_KIND_COMMAND,
		"MESSAGE_KIND_SNAPSHOT":    MessageKind_MESSAGE_KIND_SNAPSHOT,
	}
	ed := (MessageKind(0)).Descriptor()
	for name, wantValue := range wantKinds {
		value := ed.Values().ByName(protoreflect.Name(name))
		if value == nil || MessageKind(value.Number()) != wantValue {
			t.Fatalf("enum value %s mismatch", name)
		}
	}
}
