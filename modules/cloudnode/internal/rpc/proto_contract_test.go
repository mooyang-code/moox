package rpc

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestJobItemProtoContract(t *testing.T) {
	desc := (&pb.JobItem{}).ProtoReflect().Descriptor()
	mustHaveFields(t, desc, "space_id", "job_id", "job_item_id", "job_type", "code_package_id", "params", "priority")
	mustNotHaveFields(t, desc, "owner_service", "owner_ref", "idempotency_key", "payload_schema_version", "deployment_id", "lease_timeout_ms")

	listDesc := (&pb.ListJobItemsReq{}).ProtoReflect().Descriptor()
	mustHaveFields(t, listDesc, "space_id", "job_id", "job_type", "status", "page")
	mustNotHaveFields(t, listDesc, "page_size", "page_token")

	pollDesc := (&pb.PollJobItemsReq{}).ProtoReflect().Descriptor()
	mustHaveFields(t, pollDesc, "space_id", "node_id", "supported_job_types", "limit", "runtime_version", "protocol_version")
	mustNotHaveFields(t, pollDesc, "workload_type", "deployment_id", "lease_timeout_ms")
}

func mustHaveFields(t *testing.T, desc protoreflect.MessageDescriptor, names ...protoreflect.Name) {
	t.Helper()
	fields := desc.Fields()
	for _, name := range names {
		if fields.ByName(name) == nil {
			t.Fatalf("%s must expose field %q", desc.FullName(), name)
		}
	}
}

func mustNotHaveFields(t *testing.T, desc protoreflect.MessageDescriptor, names ...protoreflect.Name) {
	t.Helper()
	fields := desc.Fields()
	for _, name := range names {
		if fields.ByName(name) != nil {
			t.Fatalf("%s must not expose field %q", desc.FullName(), name)
		}
	}
}
