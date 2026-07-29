package serverless

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

func TestStorageMarketCanaryCheckEvaluatesRealRows(t *testing.T) {
	now := time.Now().UTC()
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		canaryRow(now.Add(-2*time.Minute), 100, 10),
		canaryRow(now.Add(-time.Minute), 101, 12),
	}}
	cfg := MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1h",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		AuthInfo: &storagepb.AuthInfo{AppId: "canary", AppKey: "key"},
	}
	require.True(t, StorageMarketCanaryCheck(reader, cfg)(t.Context()).Success)
	require.Equal(t, "1H", reader.request.GetSelectors()[0].GetFreq())
	require.NotNil(t, reader.request.GetSelectors()[0].SeriesTag)
	require.Equal(t, "venue:binance", reader.request.GetSelectors()[0].GetSeriesTag())
	require.Equal(t, []string{"market_kline.close", "market_kline.volume"}, reader.request.GetColumnNames())
	require.Equal(t, "crypto/market_kline/BTC-USDT/1H/venue:binance", StorageMarketCanaryCheck(reader, cfg)(t.Context()).Target)
	cfg.Freshness = time.Second
	stale := StorageMarketCanaryCheck(reader, cfg)(t.Context())
	require.False(t, stale.Success)
	require.Equal(t, "stale_watermark", stale.ErrorCode)
	require.Contains(t, stale.Error, "最新已收盘 K 线时间为")
}

func TestStorageMarketCanaryReportsObservedClosedBarCount(t *testing.T) {
	reader := &canaryReader{}
	result := StorageMarketCanaryCheck(reader, MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		AuthInfo: &storagepb.AuthInfo{AppId: "canary", AppKey: "key"},
	})(t.Context())

	require.Equal(t, "insufficient_closed_bars", result.ErrorCode)
	require.Equal(t, "Storage 查询只返回 0 根已收盘 K 线，至少需要 2 根", result.Error)
}

func TestStorageMarketCanaryOnlyComparesConfiguredSeriesTag(t *testing.T) {
	now := time.Now().UTC()
	okx := canaryRow(now.Add(-30*time.Second), 500, 500)
	okx.Key.SeriesTag = "venue:okx"
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		canaryRow(now.Add(-2*time.Minute), 100, 10),
		okx,
		canaryRow(now.Add(-time.Minute), 101, 12),
	}}
	result := StorageMarketCanaryCheck(reader, MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Frequency: "1h",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		AuthInfo: &storagepb.AuthInfo{AppId: "canary", AppKey: "key"},
	})(t.Context())

	require.True(t, result.Success)
	require.Contains(t, result.Target, "venue:binance")
}

func TestStorageMarketCanaryRejectsInvalidFrequency(t *testing.T) {
	reader := &canaryReader{}
	result := StorageMarketCanaryCheck(reader, MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "garbage",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		AuthInfo: &storagepb.AuthInfo{AppId: "canary", AppKey: "key"},
	})(t.Context())

	require.Equal(t, "invalid_config", result.ErrorCode)
	require.Nil(t, reader.request)
}

func canaryRow(at time.Time, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: "venue:binance"},
		Fields: []*storagepb.FieldValue{
			{FieldId: "market_kline.close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "market_kline.volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}

func stringPtr(value string) *string { return &value }
