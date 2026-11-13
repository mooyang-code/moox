package storagepb

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStorageCompletionEventsRoundTrip(t *testing.T) {
	now := timestamppb.Now()
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
				ViewId: "view_kline", ViewConfigId: "view-config", CompletionEventId: "period-event-1",
				DatasetId: "result_kline", Status: "degraded", VisibleScope: "universe:result:1m",
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
