package eventpb

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestStorageCompletionEventsRoundTrip(t *testing.T) {
	now := time.Now().UnixMilli()
	tests := []struct {
		name    string
		payload proto.Message
		new     func() proto.Message
	}{
		{
			name: "dataset period completed (ledger)",
			payload: &DatasetPeriodCompleted{
				DatasetId: "spot_kline", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded", Source: "ledger",
				SnapshotRef: "series-hash-1", UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"},
				FailedSubjects: []string{"ETH-USDT"},
				CompletedAt:    now,
			},
			new: func() proto.Message { return new(DatasetPeriodCompleted) },
		},
		{
			name: "dataset period completed (producer)",
			payload: &DatasetPeriodCompleted{
				DatasetId: "result_kline", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded", Source: "producer",
				UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"}, FailedSubjects: []string{"ETH-USDT"},
				CauseEventId: "collector-event-1", CompletedAt: now,
				DetailType: "moox.factor.PeriodDetail.v1", Detail: []byte{0x0a, 0x01, 0x78},
			},
			new: func() proto.Message { return new(DatasetPeriodCompleted) },
		},
		{
			name: "view data ready",
			payload: &ViewDataReady{
				CompletionEventId: "period-event-1",
				DatasetId:         "result_kline", Status: "degraded",
				Frequency: "1m", PeriodTime: 1786032000,
				ReadyAt: now, UniverseSubjectIds: []string{"BTC-USDT"},
				DetailType: "moox.factor.PeriodDetail.v1", Detail: []byte{0x0a, 0x01, 0x78},
			},
			new: func() proto.Message { return new(ViewDataReady) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := proto.Marshal(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			out := tt.new()
			if err := proto.Unmarshal(raw, out); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(tt.payload, out) {
				t.Fatalf("round-trip changed payload: in=%v out=%v", tt.payload, out)
			}
		})
	}
}

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
