package hostmetricpb

import (
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"
)

func TestHostMetricContract(t *testing.T) {
	d := (&HostMetric{}).ProtoReflect().Descriptor()
	if d.Fields().Len() != 5 || d.Fields().ByName("snapshot").Number() != 5 {
		t.Fatalf("HostMetric contract changed")
	}
	for name, number := range map[string]protoreflect.FieldNumber{"agent_id": 1, "hostname": 2, "boot_id": 3, "agent_version": 4} {
		if field := d.Fields().ByName(protoreflect.Name(name)); field == nil || field.Number() != number {
			t.Fatalf("field %s = %v, want %d", name, field, number)
		}
	}
	if (&HostSnapshot{}).ProtoReflect().Descriptor().FullName() != "trpc.moox.hostagent.HostSnapshot" {
		t.Fatalf("unexpected snapshot name")
	}
	for _, name := range []string{"sample_id", "agent_id", "generation_id", "boot_id", "sequence", "space_id", "timestamp_ms", "configured_interval_millis", "observed_elapsed_millis"} {
		if (&HostSnapshot{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name)) != nil {
			t.Fatalf("removed field %s is present", name)
		}
	}
}
