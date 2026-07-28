package watchdog

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type canaryReader struct {
	rows    []*storagepb.TimeSeriesRow
	request *storagepb.ReadTimeSeriesRowsReq
}

func (r *canaryReader) ReadTimeSeriesRows(_ context.Context, request *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	r.request = request
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    r.rows,
	}, nil
}

func TestMarketCanaryReadsRealStorageScopeAndEvaluatesClosedBars(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 101, 12),
	}}
	config := MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
	}
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.True(t, result.Success)
	require.Equal(t, "market_canary:market_kline:BTC-USDT:1m", result.CheckID)
	require.Equal(t, "crypto", reader.request.GetSpaceId())
	require.Equal(t, "market_kline", reader.request.GetDatasetId())
	require.Equal(t, "BTC-USDT", reader.request.GetKeys()[0].GetSubjectId())
	require.Equal(t, uint32(2), reader.request.GetPage().GetSize())

	reader.rows = []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 110, 12),
	}
	result = (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.False(t, result.Success)
	require.Equal(t, "threshold_exceeded", result.ErrorMessage)
}

func marketCanaryRow(at time.Time, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{
			{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}
