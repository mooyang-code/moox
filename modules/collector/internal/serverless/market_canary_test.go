package serverless

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type canaryReader struct{ rows []*storagepb.TimeSeriesRow }

func (r canaryReader) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    r.rows,
	}, nil
}

func TestStorageMarketCanaryCheckEvaluatesRealRows(t *testing.T) {
	now := time.Now().UTC()
	reader := canaryReader{rows: []*storagepb.TimeSeriesRow{
		canaryRow(now.Add(-2*time.Minute), 100, 10),
		canaryRow(now.Add(-time.Minute), 101, 12),
	}}
	cfg := MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		AuthInfo: &storagepb.AuthInfo{AppId: "canary", AppKey: "key"},
	}
	require.True(t, StorageMarketCanaryCheck(reader, cfg)(t.Context()).Success)
	cfg.Freshness = time.Second
	stale := StorageMarketCanaryCheck(reader, cfg)(t.Context())
	require.False(t, stale.Success)
	require.Equal(t, "stale_watermark", stale.ErrorCode)
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
