package storagepb

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDatasetRowsUpsertedRoundTrip(t *testing.T) {
	in := &DatasetRowsUpserted{
		SpaceId:   "crypto",
		DatasetId: "spot_kline",
		Rows: []*RowUpsert{
			{Key: &RowKey{SpaceId: "crypto", DatasetId: "spot_kline", Kind: &RowKey_TimeSeries{TimeSeries: &TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-23T00:00:00Z", SeriesTag: "venue:okx"}}}, Fields: []*FieldValue{{FieldId: "close", Value: &TypedValue{Value: &TypedValue_DoubleValue{DoubleValue: 101.25}}}}, Attributes: map[string]*TypedValue{"source": {Value: &TypedValue_StringValue{StringValue: "binance"}}}},
			{Key: &RowKey{SpaceId: "crypto", DatasetId: "spot_kline", Kind: &RowKey_Record{Record: &RecordRowKey{RecordId: "r-1", Version: "v1"}}}, Fields: []*FieldValue{{FieldId: "payload", Value: &TypedValue{Value: &TypedValue_BytesValue{BytesValue: []byte{1, 2, 3}}}}, {FieldId: "tags", Value: &TypedValue{Value: &TypedValue_ListValue{ListValue: &ValueList{Values: []*TypedValue{{Value: &TypedValue_StringValue{StringValue: "a"}}, {Value: &TypedValue_NullValue{NullValue: NullValue_NULL_VALUE_NULL}}}}}}}}},
		},
	}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := new(DatasetRowsUpserted)
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip changed payload: in=%v out=%v", in, out)
	}
}

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
				FailedSubjects:     []string{"ETH-USDT"},
				CommittedPositions: []*CommittedPosition{{NodeId: "node", StoreId: "store", Sequence: 9}},
				CompletedAt:        now,
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
				CommittedPositions: []*CommittedPosition{{NodeId: "node", StoreId: "store", Sequence: 10}},
				ReadyAt:            now, UniverseSubjectIds: []string{"BTC-USDT"},
				DetailType: "moox.factor.PeriodDetail.v1", Detail: []byte{0x0a, 0x01, 0x78},
			},
			new: func() proto.Message { return new(ViewDataReady) },
		},
		{
			name:    "dataset sync point",
			payload: &DatasetSyncPoint{SyncPointId: "sync-1", RequestId: "request-1", DatasetId: "spot_kline", Source: "import"},
			new:     func() proto.Message { return new(DatasetSyncPoint) },
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
