package watchdog

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type canaryReader struct {
	rows    []*storagepb.TimeSeriesRow
	request *storagepb.ReadTimeSeriesRowsReq
	retInfo *storagepb.RetInfo
}

type tagFilteringCanaryReader struct {
	rows    []*storagepb.TimeSeriesRow
	request *storagepb.ReadTimeSeriesRowsReq
}

func (r *tagFilteringCanaryReader) ReadTimeSeriesRows(_ context.Context, request *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	r.request = request
	selector := request.GetSelectors()[0]
	rows := make([]*storagepb.TimeSeriesRow, 0, len(r.rows))
	for _, row := range r.rows {
		if selector.SeriesTag == nil || row.GetKey().GetSeriesTag() == selector.GetSeriesTag() {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].GetKey().GetDataTime() > rows[j].GetKey().GetDataTime()
	})
	if size := int(request.GetPage().GetSize()); len(rows) > size {
		rows = rows[:size]
	}
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    rows,
	}, nil
}

func (r *canaryReader) ReadTimeSeriesRows(_ context.Context, request *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	r.request = request
	retInfo := r.retInfo
	if retInfo == nil {
		retInfo = &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
	}
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: retInfo,
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
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1h",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
	}
	auth := &commonpb.AuthInfo{AppId: "monitor-market-canary", AppKey: "derived-key"}
	result := (MarketCanary{Reader: reader, AuthInfo: auth, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.True(t, result.Success)
	require.Equal(t, "market_canary:market_kline:BTC-USDT:1h:venue:binance", result.CheckID)
	require.Equal(t, "crypto/market_kline/BTC-USDT/1h/venue:binance", MarketCanaryTarget(config))
	require.Equal(t, "crypto", reader.request.GetSpaceId())
	require.Equal(t, "market_kline", reader.request.GetDatasetId())
	require.Equal(t, "BTC-USDT", reader.request.GetSelectors()[0].GetSubjectId())
	require.Equal(t, "1H", reader.request.GetSelectors()[0].GetFreq())
	require.Equal(t, "venue:binance", reader.request.GetSelectors()[0].GetSeriesTag())
	require.NotNil(t, reader.request.GetSelectors()[0].SeriesTag)
	require.Equal(t, []string{"market_kline.close", "market_kline.volume"}, reader.request.GetColumnNames())
	require.Equal(t, uint32(2), reader.request.GetPage().GetSize())
	require.Equal(t, auth, reader.request.GetAuthInfo())

	reader.rows = []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 110, 12),
	}
	result = (MarketCanary{Reader: reader, AuthInfo: auth, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.False(t, result.Success)
	require.Equal(t, "threshold_exceeded", result.ErrorMessage)
}

func TestMarketCanaryPageDoesNotMixVenuesAtSameTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	reader := &tagFilteringCanaryReader{rows: []*storagepb.TimeSeriesRow{
		marketCanaryTaggedRow(now.Add(-2*time.Minute), "venue:binance", 100, 10),
		marketCanaryTaggedRow(now.Add(-2*time.Minute), "venue:okx", 300, 30),
		marketCanaryTaggedRow(now.Add(-time.Minute), "venue:binance", 101, 12),
		marketCanaryTaggedRow(now.Add(-time.Minute), "venue:okx", 600, 60),
	}}
	config := MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1h",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
	}
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.True(t, result.Success)
	require.Equal(t, uint32(2), reader.request.GetPage().GetSize())
	require.Equal(t, "venue:binance", reader.request.GetSelectors()[0].GetSeriesTag())
}

func TestMarketCanaryPreservesStorageRejectionDetail(t *testing.T) {
	reader := &canaryReader{retInfo: &storagepb.RetInfo{Code: 7, Msg: "dataset disabled"}}
	result := (MarketCanary{
		Reader: reader,
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "binance_spot_kline",
			SubjectID: "BTC-USDT", Frequency: "1m",
			SeriesTag: stringPtr("venue:binance"),
			Freshness: time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		},
	}).Run(t.Context())

	require.Equal(t, "storage_rejected_query:7:dataset disabled", result.ErrorMessage)
}

func TestMarketCanaryRejectsInvalidFrequency(t *testing.T) {
	reader := &canaryReader{}
	result := (MarketCanary{
		Reader: reader,
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "binance_spot_kline",
			SubjectID: "BTC-USDT", Frequency: "garbage",
			SeriesTag: stringPtr("venue:binance"),
			Freshness: time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		},
	}).Run(t.Context())

	require.Equal(t, "invalid_config", result.ErrorMessage)
	require.Nil(t, reader.request)
}

func stringPtr(value string) *string {
	return &value
}

func TestMarketCanaryStorageRejectionKeepsValidUTF8(t *testing.T) {
	message := strings.Repeat("数据集不可用", 40)
	got := storageRejectionError(&storagepb.RetInfo{Code: 7, Msg: message})
	require.True(t, utf8.ValidString(got))
	require.LessOrEqual(t, len([]rune(got)), len([]rune("storage_rejected_query:7:"))+160)
}

func TestMarketCanaryStorageRejectionRedactsSensitiveDetail(t *testing.T) {
	for _, message := range []string{
		"query failed token=super-secret-value",
		"query failed secret: super-secret-value",
		"open /home/ubuntu/moox/storage/data/private.db: permission denied",
		"open /root/moox/storage/private.db: permission denied",
		`open C:\moox\storage\private.db: permission denied`,
	} {
		got := storageRejectionError(&storagepb.RetInfo{Code: 7, Msg: message})
		require.NotContains(t, got, "super-secret-value")
		require.NotContains(t, got, "/home/ubuntu")
		require.Contains(t, got, "details_redacted")
	}
}

func marketCanaryRow(at time.Time, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return marketCanaryTaggedRow(at, "", closeValue, volumeValue)
}

func marketCanaryTaggedRow(at time.Time, seriesTag string, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag},
		Fields: []*storagepb.FieldValue{
			{FieldId: "market_kline.close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "market_kline.volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}
