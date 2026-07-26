package storageio

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestRowsToDataFrameOrdersByCandleBeginTime(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 9, 14, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	rows := []*storagepb.TimeSeriesRow{
		klineRow(t1, map[string]*storagepb.FieldValue{
			"open":         doubleField("open", 10),
			"high":         doubleField("high", 11),
			"low":          doubleField("low", 9),
			"close":        doubleField("close", 10.5),
			"volume":       doubleField("volume", 100),
			"quote_volume": doubleField("quote_volume", 1000),
			"trade_num":    intField("trade_num", 7),
		}),
		klineRow(t0, map[string]*storagepb.FieldValue{
			"open":         doubleField("open", 1),
			"high":         doubleField("high", 2),
			"low":          doubleField("low", 0.5),
			"close":        doubleField("close", 1.5),
			"volume":       doubleField("volume", 10),
			"quote_volume": doubleField("quote_volume", 100),
			"trade_num":    intField("trade_num", 3),
		}),
	}

	frame, err := RowsToDataFrame(rows, KLineColumns)
	if err != nil {
		t.Fatalf("RowsToDataFrame() error = %v", err)
	}
	if !reflect.DeepEqual(frame.DataTimes, []time.Time{t0, t1}) {
		t.Fatalf("data times = %#v", frame.DataTimes)
	}
	if !reflect.DeepEqual(frame.Columns, KLineColumns) {
		t.Fatalf("columns = %#v", frame.Columns)
	}
	if !reflect.DeepEqual(frame.Rows[0], []any{1.0, 2.0, 0.5, 1.5, 10.0, 100.0, int64(3)}) {
		t.Fatalf("first row = %#v", frame.Rows[0])
	}
}

func TestWriteFactorPatchMapsTailAndOmitsNilValues(t *testing.T) {
	t0 := time.Date(2026, 7, 6, 9, 14, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t1.Add(time.Minute)
	access := &fakeAccessClient{}
	client := NewClientWithAccess(access, nil)

	err := client.WriteFactorPatch(context.Background(), &engine.FactorTask{
		SpaceID:       "crypto",
		TargetDataset: "binance_spot_factor",
		SubjectID:     "BTC-USDT",
		Freq:          "1m",
	}, &engine.DataFrame{DataTimes: []time.Time{t0, t1, t2}}, &engine.FactorResult{
		Columns: map[string]engine.FactorColumnResult{
			"Bias_20": {Tail: 2, Values: []any{nil, 1.23}},
		},
	})
	if err != nil {
		t.Fatalf("WriteFactorPatch() error = %v", err)
	}
	if len(access.writeReqs) != 1 {
		t.Fatalf("write requests = %d", len(access.writeReqs))
	}
	rows := access.writeReqs[0].GetRows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].GetKey().GetTimeSeries().GetDataTime() != t1.Format(time.RFC3339) || len(rows[0].GetFields()) != 0 {
		t.Fatalf("nil row should preserve key and omit columns: %+v", rows[0])
	}
	if rows[1].GetKey().GetTimeSeries().GetDataTime() != t2.Format(time.RFC3339) {
		t.Fatalf("second row key = %s", rows[1].GetKey().GetTimeSeries().GetDataTime())
	}
	if got := rows[1].GetFields()[0].GetValue().GetDoubleValue(); got != 1.23 {
		t.Fatalf("written value = %v", got)
	}
}

func TestTypedValueToAnyCoversValueTypesAndFallbacks(t *testing.T) {
	tests := []struct {
		name  string
		value *storagepb.TypedValue
		want  any
	}{
		{name: "nil value", value: nil, want: nil},
		{name: "int", value: intField("n", 7).GetValue(), want: int64(7)},
		{name: "double", value: doubleField("n", 1.5).GetValue(), want: 1.5},
		{name: "string", value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "x"}}, want: "x"},
		{name: "bool", value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: true}}, want: true},
		{name: "time", value: &storagepb.TypedValue{Value: &storagepb.TypedValue_TimeValue{TimeValue: "2026-07-12T00:00:00Z"}}, want: "2026-07-12T00:00:00Z"},
		{name: "json", value: &storagepb.TypedValue{Value: &storagepb.TypedValue_JsonValue{JsonValue: `{"a":1}`}}, want: `{"a":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, typedValueToAny(tt.value))
		})
	}
}

func TestValueForFrameRowAndAsFloat64CoverEdges(t *testing.T) {
	value, ok := valueForFrameRow(2, 3, engine.FactorColumnResult{Tail: 2, Values: []any{1, 2}})
	require.True(t, ok)
	assert.Equal(t, 2, value)

	_, ok = valueForFrameRow(0, 3, engine.FactorColumnResult{Tail: 2, Values: []any{1, 2}})
	assert.False(t, ok)
	_, ok = valueForFrameRow(2, 3, engine.FactorColumnResult{Tail: 1, Values: nil})
	assert.False(t, ok)

	for _, input := range []any{float64(1), float32(2), int(3), int64(4), int32(5)} {
		got, ok := asFloat64(input)
		require.True(t, ok)
		assert.NotZero(t, got)
	}
	_, ok = asFloat64("bad")
	assert.False(t, ok)
}

type fakeAccessClient struct {
	writeReqs []*storagepb.PrimaryUpsertFieldsReq
}

func (f *fakeAccessClient) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: successRet()}, nil
}

func (f *fakeAccessClient) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	f.writeReqs = append(f.writeReqs, req)
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: successRet()}, nil
}

func klineRow(t time.Time, columns map[string]*storagepb.FieldValue) *storagepb.TimeSeriesRow {
	values := make([]*storagepb.FieldValue, 0, len(columns))
	for _, col := range columns {
		values = append(values, col)
	}
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{
			SpaceId:   "crypto",
			DatasetId: "binance_spot_kline",
			SubjectId: "BTC-USDT",
			Freq:      "1m",
			DataTime:  t.Format(time.RFC3339),
		},
		Fields: values,
	}
}

func successRet() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}
