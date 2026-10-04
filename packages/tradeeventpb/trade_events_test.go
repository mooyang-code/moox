package tradeeventpb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestTradeEventsExposeOnlyModernWeightTargetContract(t *testing.T) {
	for _, name := range []protoreflect.Name{"InstrumentTarget", "LogicalAccountTargetRequested"} {
		t.Run(string(name), func(t *testing.T) {
			if descriptor := File_trade_events_proto.Messages().ByName(name); descriptor != nil {
				t.Errorf("obsolete message %s is still present", name)
			}
		})
	}

	message := (&LogicalAccountTargetWeightRequested{}).ProtoReflect().Descriptor()
	for _, field := range []struct {
		name   protoreflect.Name
		number protoreflect.FieldNumber
	}{
		{name: "command_sequence", number: 4},
		{name: "signal_time", number: 6},
		{name: "owner_generation", number: 7},
		{name: "runner_id", number: 13},
	} {
		t.Run(string(field.name), func(t *testing.T) {
			if message.Fields().ByName(field.name) != nil {
				t.Errorf("obsolete field %s is still present", field.name)
			}
			if message.Fields().ByNumber(field.number) != nil {
				t.Errorf("obsolete field number %d is still present", field.number)
			}
		})
	}

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
		{name: "effective_at", number: 11},
		{name: "valid_until", number: 12},
	} {
		t.Run(string(field.name), func(t *testing.T) {
			descriptor := message.Fields().ByName(field.name)
			if descriptor == nil {
				t.Fatalf("modern field %s is missing", field.name)
			}
			if descriptor.Number() != field.number {
				t.Errorf("modern field %s has number %d, want %d", field.name, descriptor.Number(), field.number)
			}
		})
	}
}
