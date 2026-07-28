package test

import (
	"context"
	"testing"
	"time"

	monitorwatchdog "github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type storageCanaryFixture struct {
	request *storagepb.ReadTimeSeriesRowsReq
	rows    []*storagepb.TimeSeriesRow
}

func (f *storageCanaryFixture) ReadTimeSeriesRows(_ context.Context, request *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.request = request
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    f.rows,
	}, nil
}

func TestMarketCanaryUsesStoragePrimaryReadContract(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	fixture := &storageCanaryFixture{rows: []*storagepb.TimeSeriesRow{
		canaryRow(now.Add(-2*time.Minute), 100, 10),
		canaryRow(now.Add(-time.Minute), 101, 12),
	}}
	result := (monitorwatchdog.MarketCanary{
		Reader: fixture,
		Config: monitorwatchdog.MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
			Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		},
		Now: func() time.Time { return now },
	}).Run(t.Context())

	require.True(t, result.Success)
	require.NotNil(t, fixture.request)
	require.Equal(t, storagepb.SortOrder_SORT_ORDER_DESC, fixture.request.GetOrder())
	require.Equal(t, []string{"close", "volume"}, fixture.request.GetColumnNames())
	require.Equal(t, uint32(2), fixture.request.GetPage().GetSize())
	require.Equal(t, "crypto", fixture.request.GetKeys()[0].GetSpaceId())
	require.Equal(t, "market_kline", fixture.request.GetKeys()[0].GetDatasetId())
	require.Equal(t, "BTC-USDT", fixture.request.GetKeys()[0].GetSubjectId())
	require.Equal(t, "1m", fixture.request.GetKeys()[0].GetFreq())
}

func canaryRow(at time.Time, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{
			{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}
