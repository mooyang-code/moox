package rpc

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestProtoContract_CollectorMessagesUseExplicitSpaceStructsAndEnums(t *testing.T) {
	rule := (&pb.TaskRule{}).ProtoReflect().Descriptor()
	requireFieldNumber(t, rule, "space_id", 1)
	requireMessageKind(t, rule, "collect_params")
	requireRepeatedString(t, rule, "assigned_nodes")
	requireRepeatedString(t, rule, "node_tags")
	requireKind(t, rule, "enabled", protoreflect.BoolKind)

	instance := (&pb.TaskInstance{}).ProtoReflect().Descriptor()
	requireFieldNumber(t, instance, "space_id", 1)
	requireFieldNumber(t, instance, "task_id", 2)
	requireFieldNumber(t, instance, "exchange", 4)
	requireFieldNumber(t, instance, "interval", 10)
	requireMessageKind(t, instance, "task_params")
	requireEnumKind(t, instance, "last_exec_status")
	requireMessageKind(t, instance, "result")
	if field := instance.Fields().ByName("cloud_work_item_id"); field != nil {
		t.Fatalf("TaskInstance must not expose cloud_work_item_id")
	}
	if field := instance.Fields().ByName("collect_data_type"); field != nil {
		t.Fatalf("TaskInstance must not expose collect_data_type")
	}

	filter := (&pb.TaskInstanceFilter{}).ProtoReflect().Descriptor()
	requireFieldNumber(t, filter, "space_id", 1)
	requireEnumKind(t, filter, "last_exec_status")
	requireKind(t, filter, "include_deleted", protoreflect.BoolKind)
	requireMessageKind(t, filter, "page")
	if field := filter.Fields().ByName("cloud_work_item_id"); field != nil {
		t.Fatalf("TaskInstanceFilter must not expose cloud_work_item_id")
	}

	report := (&pb.ReportInstanceStatusReq{}).ProtoReflect().Descriptor()
	requireFieldNumber(t, report, "space_id", 1)
	requireFieldNumber(t, report, "task_id", 2)
	requireEnumKind(t, report, "status")
	requireMessageFullName(t, report, "result", "google.protobuf.Struct")
	if field := report.Fields().ByName("instance_id"); field != nil {
		t.Fatalf("ReportInstanceStatusReq must use task_id instead of instance_id")
	}

	recalculate := (&pb.RecalculateAllTaskInstancesReq{}).ProtoReflect().Descriptor()
	requireFieldNumber(t, recalculate, "space_id", 1)
}

func TestProtoContract_DynamicFieldsUseStructOrValue(t *testing.T) {
	rule := (&pb.TaskRule{}).ProtoReflect().Descriptor()
	requireMessageFullName(t, rule, "collect_params", "google.protobuf.Struct")

	instance := (&pb.TaskInstance{}).ProtoReflect().Descriptor()
	requireMessageFullName(t, instance, "task_params", "google.protobuf.Struct")
	requireMessageFullName(t, instance, "result", "google.protobuf.Struct")

	dataType := (&pb.DataTypeConfig{}).ProtoReflect().Descriptor()
	requireMessageFullName(t, dataType, "data_source_options", "google.protobuf.Struct")

	field := (&pb.DataTypeFieldConfig{}).ProtoReflect().Descriptor()
	requireMessageFullName(t, field, "field_options", "google.protobuf.Struct")
	requireMessageFullName(t, field, "data_source_options", "google.protobuf.Struct")
	requireMessageFullName(t, field, "default_value", "google.protobuf.Value")
}

func TestProtoContract_ListResponsesUseCommonPageResult(t *testing.T) {
	rules := (&pb.GetTaskRuleListRsp{}).ProtoReflect().Descriptor()
	requireMessageKind(t, rules, "page")
	if field := rules.Fields().ByName("total"); field != nil {
		t.Fatalf("GetTaskRuleListRsp must use common.PageResult instead of total")
	}

	instances := (&pb.GetTaskInstanceListRsp{}).ProtoReflect().Descriptor()
	requireMessageKind(t, instances, "page")
	if field := instances.Fields().ByName("total"); field != nil {
		t.Fatalf("GetTaskInstanceListRsp must use common.PageResult instead of total")
	}
}

func requireFieldNumber(t *testing.T, msg protoreflect.MessageDescriptor, name protoreflect.Name, number protoreflect.FieldNumber) {
	t.Helper()
	field := msg.Fields().ByName(name)
	if field == nil {
		t.Fatalf("%s missing field %s", msg.FullName(), name)
	}
	if field.Number() != number {
		t.Fatalf("%s.%s number = %d, want %d", msg.FullName(), name, field.Number(), number)
	}
}

func requireKind(t *testing.T, msg protoreflect.MessageDescriptor, name protoreflect.Name, kind protoreflect.Kind) {
	t.Helper()
	field := msg.Fields().ByName(name)
	if field == nil {
		t.Fatalf("%s missing field %s", msg.FullName(), name)
	}
	if field.Kind() != kind {
		t.Fatalf("%s.%s kind = %s, want %s", msg.FullName(), name, field.Kind(), kind)
	}
}

func requireMessageKind(t *testing.T, msg protoreflect.MessageDescriptor, name protoreflect.Name) {
	t.Helper()
	requireKind(t, msg, name, protoreflect.MessageKind)
}

func requireMessageFullName(t *testing.T, msg protoreflect.MessageDescriptor, name protoreflect.Name, fullName protoreflect.FullName) {
	t.Helper()
	requireMessageKind(t, msg, name)
	field := msg.Fields().ByName(name)
	if field.Message().FullName() != fullName {
		t.Fatalf("%s.%s message = %s, want %s", msg.FullName(), name, field.Message().FullName(), fullName)
	}
}

func requireEnumKind(t *testing.T, msg protoreflect.MessageDescriptor, name protoreflect.Name) {
	t.Helper()
	requireKind(t, msg, name, protoreflect.EnumKind)
}

func requireRepeatedString(t *testing.T, msg protoreflect.MessageDescriptor, name protoreflect.Name) {
	t.Helper()
	field := msg.Fields().ByName(name)
	if field == nil {
		t.Fatalf("%s missing field %s", msg.FullName(), name)
	}
	if !field.IsList() || field.Kind() != protoreflect.StringKind {
		t.Fatalf("%s.%s must be repeated string, got list=%v kind=%s", msg.FullName(), name, field.IsList(), field.Kind())
	}
}
