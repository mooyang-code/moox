package metricspb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestMetricSnapshotDescriptorContract(t *testing.T) {
	d := (&MetricSnapshot{}).ProtoReflect().Descriptor()
	want := map[string]protoreflect.FieldNumber{
		"schema_version": 1, "collection_interval_seconds": 2, "format": 3,
		"compression": 4, "data": 5, "metric_family_count": 6,
		"sample_count": 7, "uncompressed_sha256": 8,
	}
	if d.Fields().Len() != len(want) {
		t.Fatalf("field count = %d, want %d", d.Fields().Len(), len(want))
	}
	for name, number := range want {
		field := d.Fields().ByName(protoreflect.Name(name))
		if field == nil || field.Number() != number {
			t.Fatalf("field %s = %v, want %d", name, field, number)
		}
	}
	for _, forbidden := range []string{"service_name", "instance_id", "sequence", "space_id", "occurred_at"} {
		if d.Fields().ByName(protoreflect.Name(forbidden)) != nil {
			t.Fatalf("snapshot must not own envelope field %s", forbidden)
		}
	}
	for _, item := range []struct {
		enum   protoreflect.EnumDescriptor
		values []struct {
			name   string
			number protoreflect.EnumNumber
		}
	}{
		{d.Fields().ByName("format").Enum(), []struct {
			name   string
			number protoreflect.EnumNumber
		}{{"EXPOSITION_FORMAT_UNSPECIFIED", 0}, {"EXPOSITION_FORMAT_PROMETHEUS_TEXT", 1}}},
		{d.Fields().ByName("compression").Enum(), []struct {
			name   string
			number protoreflect.EnumNumber
		}{{"COMPRESSION_UNSPECIFIED", 0}, {"COMPRESSION_NONE", 1}, {"COMPRESSION_GZIP", 2}}},
	} {
		for _, value := range item.values {
			got := item.enum.Values().ByName(protoreflect.Name(value.name))
			if got == nil || got.Number() != value.number {
				t.Fatalf("enum %s = %v, want %d", value.name, got, value.number)
			}
		}
	}
}
