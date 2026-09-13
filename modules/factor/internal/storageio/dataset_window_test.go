package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestDatasetWindowRejectsFutureRows(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	end := period.Add(time.Minute)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{
		datasetWindowRow("BTC-USDT", period.Add(time.Minute), 1),
	}}
	_, err := (&Client{primary: stub, auth: &commonpb.AuthInfo{AppId: "moox-factor-engine"}}).ReadDatasetWindow(
		context.Background(), WindowKey{SpaceID: "crypto", SourceDataset: "mdataset_binance_kline_1m", SubjectID: "BTC-USDT", Freq: "1m"},
		period, end, 1, []string{"close"},
	)
	require.ErrorContains(t, err, "future")
}

func TestDatasetWindowEmptySubjectsDoNotScan(t *testing.T) {
	stub := &datasetPrimaryStub{}
	chunks, err := (&Client{primary: stub, auth: &commonpb.AuthInfo{AppId: "moox-factor-engine"}}).ReadPeriodChunks(
		context.Background(),
		WindowKey{SpaceID: "crypto", SourceDataset: "mdataset_binance_kline_1m", Freq: "1m"},
		nil,
		time.Unix(60, 0).UTC(), time.Unix(120, 0).UTC(), 20, []string{"close"},
	)
	require.NoError(t, err)
	require.Empty(t, chunks)
	require.Empty(t, stub.reqs)
}

func TestDatasetWindowMissingHistoryIsExplicit(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{
		datasetWindowRow("BTC-USDT", period, 1),
	}}
	chunk, err := (&Client{primary: stub, auth: &commonpb.AuthInfo{AppId: "moox-factor-engine"}}).ReadDatasetWindow(
		context.Background(), WindowKey{SpaceID: "crypto", SourceDataset: "mdataset_binance_kline_1m", SubjectID: "BTC-USDT", Freq: "1m"},
		period, period.Add(time.Minute), 3, []string{"close"},
	)
	require.NoError(t, err)
	require.ErrorIs(t, RequireDatasetLookback(chunk, 3), ErrInsufficientHistory)
	require.Len(t, stub.reqs, 1)
	require.Len(t, stub.reqs[0].GetKeys(), 3)
	require.Nil(t, stub.reqs[0].GetTimeRange())
	require.NotEmpty(t, stub.reqs[0].GetColumnNames())
}

func TestDatasetWindowPreservesJSONNullAndIntegerPrecision(t *testing.T) {
	period := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	stub := &datasetPrimaryStub{rows: []*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "mdataset_binance_kline_1m", SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{
			{FieldId: "flag", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}},
			{FieldId: "qty", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: 9007199254740993}}},
			{FieldId: "meta", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_JsonValue{JsonValue: `{"k":1}`}}},
		},
	}}}
	chunk, err := (&Client{primary: stub, auth: &commonpb.AuthInfo{AppId: "moox-factor-engine"}}).ReadDatasetWindow(
		context.Background(), WindowKey{SpaceID: "crypto", SourceDataset: "mdataset_binance_kline_1m", SubjectID: "BTC-USDT", Freq: "1m"},
		period, period.Add(time.Minute), 1, []string{"flag", "qty", "meta"},
	)
	require.NoError(t, err)
	require.NoError(t, RequireDatasetLookback(chunk, 1))
	require.Equal(t, []any{nil, int64(9007199254740993), `{"k":1}`}, chunk.Frame.Rows[0])
	require.False(t, chunk.Frame.DataTimes[0].After(period))
}

type datasetPrimaryStub struct {
	reqs []*storagepb.ReadTimeSeriesRowsReq
	rows []*storagepb.TimeSeriesRow
}

func (s *datasetPrimaryStub) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	s.reqs = append(s.reqs, req)
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, Rows: s.rows}, nil
}

func datasetWindowRow(subject string, at time.Time, close float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "mdataset_binance_kline_1m", SubjectId: subject, Freq: "1m", DataTime: at.Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{{
			FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: close}},
		}},
	}
}

func TestDatasetWindowRejectsDuplicateAndOutOfUniverseOutputs(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	task := &engine.FactorTask{
		SubjectID: "BTC-USDT", Factor: engine.FactorSpec{FactorType: "timeseries", FactorID: "bias", Outputs: []string{"bias"}},
		AvailableSubjects: []string{"BTC-USDT"}, SpaceID: "crypto", ResultDatasetID: "mdataset_binance_kline_1m", Freq: "1m",
	}
	require.ErrorContains(t, ValidateDatasetOutputs(task, &engine.FactorResult{Rows: []engine.FactorResultRow{
		{SubjectID: "ETH-USDT", DataTime: at, Values: map[string]any{"bias": 1.0}},
	}}), "outside")
	cs := *task
	cs.Factor.FactorType = "cross_section"
	cs.AvailableSubjects = []string{"BTC-USDT", "ETH-USDT"}
	cs.SubjectID = ""
	require.ErrorContains(t, ValidateDatasetOutputs(&cs, &engine.FactorResult{Rows: []engine.FactorResultRow{
		{SubjectID: "BTC-USDT", DataTime: at, Values: map[string]any{"bias": 1.0}},
		{SubjectID: "BTC-USDT", DataTime: at, Values: map[string]any{"bias": 2.0}},
	}}), "duplicate")
}
