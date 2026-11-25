package tradeeventpb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// 目标权重事件的字段号是 Strategy 与 Trade 之间的线上契约，改号会让两边解析错位。
func TestTargetWeightRequestedFieldNumbers(t *testing.T) {
	message := (&LogicalAccountTargetWeightRequested{}).ProtoReflect().Descriptor()
	for _, field := range []struct {
		name   protoreflect.Name
		number protoreflect.FieldNumber
	}{
		{name: "target_id", number: 1},
		{name: "instance_id", number: 2},
		{name: "logical_account_id", number: 3},
		{name: "targets", number: 5},
		{name: "session_id", number: 8},
		{name: "strategy_id", number: 9},
		{name: "bar_end_time", number: 10},
		{name: "valid_until", number: 12},
		{name: "shadow", number: 14},
		{name: "session_started_at", number: 15},
	} {
		t.Run(string(field.name), func(t *testing.T) {
			descriptor := message.Fields().ByName(field.name)
			if descriptor == nil {
				t.Fatalf("字段 %s 不存在", field.name)
			}
			if descriptor.Number() != field.number {
				t.Errorf("字段 %s 的编号是 %d，应为 %d", field.name, descriptor.Number(), field.number)
			}
		})
	}
}
