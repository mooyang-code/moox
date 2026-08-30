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
			name: "dataset period collected",
			payload: &DatasetPeriodCollected{
				DatasetId: "spot_kline", Frequency: "1m", PeriodTime: 1786032000,
				Status: "degraded", SubjectIds: []string{"BTC-USDT", "ETH-USDT"},
				FailedSubjects: []string{"ETH-USDT"}, CollectedAt: now,
			},
			new: func() proto.Message { return new(DatasetPeriodCollected) },
		},
		{
			name: "view source period ready",
			payload: &ViewSourcePeriodReady{
				SourceViewId: "source_view", Frequency: "1m", PeriodTime: 1786032000,
				Status: "degraded", Datasets: []*ViewPeriodDatasetState{{
					DatasetId: "spot_kline", Status: "degraded", FailedSubjects: []string{"ETH-USDT"},
				}}, PrimarySubjects: []string{"BTC-USDT"}, ReadyAt: now,
			},
			new: func() proto.Message { return new(ViewSourcePeriodReady) },
		},
		{
			name: "factor period computed",
			payload: &FactorPeriodComputed{
				SourceViewId: "source_view", ResultDatasetId: "factor_result", Frequency: "1m",
				PeriodTime: 1786032000, Status: "degraded", Bindings: []*FactorBindingPeriodState{{
					BindingId: "binding-1", FactorId: "factor-1", Status: "degraded", SourceHash: "hash-1",
					SkippedSubjects: []string{"ETH-USDT"}, FailedSubjects: []string{"BTC-USDT"},
				}}, ComputedAt: now, TriggerEventId: "source-ready-1",
			},
			new: func() proto.Message { return new(FactorPeriodComputed) },
		},
		{
			name: "view factor period ready",
			payload: &ViewFactorPeriodReady{
				SourceViewId: "source_view", ResultViewId: "result_view", Frequency: "1m",
				PeriodTime: 1786032000, Status: "complete", Bindings: []*FactorBindingPeriodState{{
					BindingId: "binding-1", FactorId: "factor-1", Status: "complete", SourceHash: "hash-1",
				}}, ReadyAt: now,
			},
			new: func() proto.Message { return new(ViewFactorPeriodReady) },
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
