package watchdog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type canaryReader struct {
	rows    []*storagepb.TimeSeriesRow
	request *storagepb.PrimaryReadRowsReq
	retInfo *storagepb.RetInfo
}

type tagFilteringCanaryReader struct {
	rows    []*storagepb.TimeSeriesRow
	request *storagepb.PrimaryReadRowsReq
}

func rowFieldValues(row *storagepb.TimeSeriesRow) *storagepb.RowFieldValues {
	key := row.GetKey()
	return &storagepb.RowFieldValues{
		Key: &storagepb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag(),
		}}},
		Fields: row.GetFields(),
	}
}

func (r *tagFilteringCanaryReader) ReadRows(_ context.Context, request *storagepb.PrimaryReadRowsReq, _ ...client.Option) (*storagepb.PrimaryReadRowsRsp, error) {
	r.request = request
	wanted := make(map[string]struct{}, len(request.GetKeys()))
	for _, key := range request.GetKeys() {
		wanted[key.GetTimeSeries().GetDataTime()+"\n"+key.GetTimeSeries().GetSeriesTag()] = struct{}{}
	}
	rows := make([]*storagepb.RowFieldValues, 0, len(r.rows))
	for _, row := range r.rows {
		if _, ok := wanted[row.GetKey().GetDataTime()+"\n"+row.GetKey().GetSeriesTag()]; ok {
			rows = append(rows, rowFieldValues(row))
		}
	}
	return &storagepb.PrimaryReadRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    rows,
	}, nil
}

func (r *canaryReader) ReadRows(_ context.Context, request *storagepb.PrimaryReadRowsReq, _ ...client.Option) (*storagepb.PrimaryReadRowsRsp, error) {
	r.request = request
	retInfo := r.retInfo
	if retInfo == nil {
		retInfo = &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
	}
	wanted := make(map[string]struct{}, len(request.GetKeys()))
	for _, key := range request.GetKeys() {
		wanted[key.GetTimeSeries().GetDataTime()] = struct{}{}
	}
	rows := make([]*storagepb.RowFieldValues, 0, len(r.rows))
	for _, row := range r.rows {
		if _, ok := wanted[row.GetKey().GetDataTime()]; ok {
			rows = append(rows, rowFieldValues(row))
		}
	}
	return &storagepb.PrimaryReadRowsRsp{
		RetInfo: retInfo,
		Rows:    rows,
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
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05,
	}
	auth := &commonpb.AuthInfo{AppId: "monitor-market-canary", AppKey: "derived-key"}
	result := (MarketCanary{Reader: reader, AuthInfo: auth, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.True(t, result.Success)
	require.Equal(t, "market_canary:market_kline:BTC-USDT:1m:venue:binance", result.CheckID)
	require.Equal(t, "crypto/market_kline/BTC-USDT/1m/venue:binance", MarketCanaryTarget(config))
	require.Equal(t, "crypto", reader.request.GetKeys()[0].GetSpaceId())
	require.Equal(t, "market_kline", reader.request.GetKeys()[0].GetDatasetId())
	require.Equal(t, "BTC-USDT", reader.request.GetKeys()[0].GetTimeSeries().GetSubjectId())
	require.Equal(t, "1m", reader.request.GetKeys()[0].GetTimeSeries().GetFreq())
	require.Equal(t, "venue:binance", reader.request.GetKeys()[0].GetTimeSeries().GetSeriesTag())
	// 只取已收盘的桶（不含 12:00 这根未收盘的），按时间升序。
	require.Len(t, reader.request.GetKeys(), 24)
	require.Equal(t, now.Add(-24*time.Minute).Format(time.RFC3339Nano), reader.request.GetKeys()[0].GetTimeSeries().GetDataTime())
	require.Equal(t, now.Add(-time.Minute).Format(time.RFC3339Nano), reader.request.GetKeys()[23].GetTimeSeries().GetDataTime())
	require.Equal(t, []string{"close"}, reader.request.GetColumnNames())
	require.Equal(t, auth, reader.request.GetAuthInfo())

	reader.rows = []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 110, 12),
	}
	result = (MarketCanary{Reader: reader, AuthInfo: auth, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.False(t, result.Success)
	require.Equal(t, "相邻K线收盘价从 100 变为 110，波动 10.00%，超过 5.00% 阈值", result.ErrorMessage)
	var diagnostic marketCanaryThresholdDiagnostic
	require.NoError(t, json.Unmarshal([]byte(result.BodyExcerpt), &diagnostic))
	require.Equal(t, "market_canary_threshold", diagnostic.Type)
	require.Equal(t, 100.0, diagnostic.PreviousClose)
	require.Equal(t, 110.0, diagnostic.CurrentClose)
	require.InDelta(t, 0.1, diagnostic.PriceReturn, 1e-12)
}

func TestMarketCanaryIgnoresVolumeChanges(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 100.1, 5000),
	}}
	config := MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "THIN-USDT", Frequency: "1m",
		SeriesTag: stringPtr("venue:binance"), Freshness: 3 * time.Minute, ReturnThreshold: 0.05,
	}

	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.True(t, result.Success)
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
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		SeriesTag: stringPtr("venue:binance"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05,
	}
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.True(t, result.Success)
	require.Len(t, reader.request.GetKeys(), 24)
	require.Equal(t, "venue:binance", reader.request.GetKeys()[0].GetTimeSeries().GetSeriesTag())
}

func TestMarketCanaryPreservesStorageRejectionDetail(t *testing.T) {
	reader := &canaryReader{retInfo: &storagepb.RetInfo{Code: 7, Msg: "dataset disabled"}}
	result := (MarketCanary{
		Reader: reader,
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h",
			SubjectID: "BTC-USDT", Frequency: "1m",
			SeriesTag: stringPtr("venue:binance"),
			Freshness: time.Minute, ReturnThreshold: 0.05,
		},
	}).Run(t.Context())

	require.Equal(t, "storage_rejected_query:7:dataset disabled", result.ErrorMessage)
}

func TestMarketCanaryExactWindowCoversConfiguredFreshness(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-31*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-30*time.Minute), 101, 12),
	}}
	result := (MarketCanary{
		Reader: reader,
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
			SeriesTag: stringPtr("venue:binance"),
			Freshness: 150 * time.Minute, ReturnThreshold: 0.05,
		},
		Now: func() time.Time { return now },
	}).Run(t.Context())

	require.True(t, result.Success)
	require.Len(t, reader.request.GetKeys(), 152)
}

func TestMarketCanaryRejectsInvalidFrequency(t *testing.T) {
	reader := &canaryReader{}
	result := (MarketCanary{
		Reader: reader,
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "dataset_spot_kline_1h",
			SubjectID: "BTC-USDT", Frequency: "garbage",
			SeriesTag: stringPtr("venue:binance"),
			Freshness: time.Minute, ReturnThreshold: 0.05,
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

func TestMarketCanaryProbeDetectsPrimaryAuthRejection(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	reader := &canaryReader{retInfo: &storagepb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION, Msg: "invalid primary auth"}}
	canary := MarketCanary{
		Reader:   reader,
		AuthInfo: &commonpb.AuthInfo{AppId: "monitor-market-canary", AppKey: "stale-key"},
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
			SeriesTag: stringPtr("venue:binance"),
		},
		Now: func() time.Time { return now },
	}
	err := canary.ProbeStorageAuth(t.Context())
	require.Error(t, err)
	require.True(t, IsStorageAuthError(err))
	require.Contains(t, err.Error(), "invalid primary auth")
}

func TestMarketCanaryProbeAcceptsEmptyStorageResponse(t *testing.T) {
	canary := MarketCanary{
		Reader:   &canaryReader{retInfo: &storagepb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}},
		AuthInfo: &commonpb.AuthInfo{AppId: "monitor-market-canary", AppKey: "key"},
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
			SeriesTag: stringPtr("venue:binance"),
		},
	}
	require.NoError(t, canary.ProbeStorageAuth(t.Context()))
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

func TestMarketCanaryStockCNChecksLatestClosedBucketsAndOHLCV(t *testing.T) {
	now := shanghai(2026, 8, 28, 9, 33, 5)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(shanghai(2026, 8, 28, 9, 30, 0), "sina"),
		stockCanaryRow(shanghai(2026, 8, 28, 9, 31, 0), "tencent"),
		stockCanaryRow(shanghai(2026, 8, 28, 9, 32, 0), "eastmoney"),
	}}
	config := stockCanaryConfig("1m")
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.True(t, result.Success)
	require.Equal(t, []string{"open", "high", "low", "close", "volume", "amount", "source_provider"}, reader.request.GetColumnNames())
	require.Equal(t, []string{"2026-08-28T01:30:00Z", "2026-08-28T01:31:00Z", "2026-08-28T01:32:00Z"}, requestedTimes(reader))
	require.Equal(t, "default", reader.request.GetKeys()[0].GetTimeSeries().GetSeriesTag())
	require.Equal(t, "1m", reader.request.GetKeys()[0].GetTimeSeries().GetFreq())
}

func TestMarketCanaryStockCNChecksFinalBarDuringPostCloseGrace(t *testing.T) {
	config := stockCanaryConfig("1m")
	early := shanghai(2026, 8, 28, 15, 0, 5)
	earlyReader := &canaryReader{}
	earlyResult := (MarketCanary{Reader: earlyReader, Config: config, Now: func() time.Time { return early }}).Run(t.Context())
	require.True(t, earlyResult.Success)
	require.JSONEq(t, `{"state":"idle"}`, earlyResult.BodyExcerpt)
	require.Nil(t, earlyReader.request)

	now := shanghai(2026, 8, 28, 15, 1, 5)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(shanghai(2026, 8, 28, 14, 57, 0), "sina"),
		stockCanaryRow(shanghai(2026, 8, 28, 14, 58, 0), "sina"),
		stockCanaryRow(shanghai(2026, 8, 28, 14, 59, 0), "sina"),
	}}
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.True(t, result.Success)
	require.Equal(t, []string{"2026-08-28T06:57:00Z", "2026-08-28T06:58:00Z", "2026-08-28T06:59:00Z"}, requestedTimes(reader))
}

func TestMarketCanaryStockCNMiddayAndClosedDayAreIdleHealthy(t *testing.T) {
	requireTradingStatus(t, 2026, 10, 1, marketcalendar.NonTradingDay)
	config := stockCanaryConfig("1m")
	for _, now := range []time.Time{
		shanghai(2026, 8, 28, 12, 0, 0), // 午休
		shanghai(2026, 10, 1, 10, 0, 0), // 国庆休市
	} {
		reader := &canaryReader{}
		result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
		require.True(t, result.Success)
		require.JSONEq(t, `{"state":"idle"}`, result.BodyExcerpt)
		require.Nil(t, reader.request)
	}
}

// 5m 探针在交易时段内检查最近 3 根已收盘的 5 分钟线；午休前、收盘后只在收盘后 1～4 分钟内检查该时段最后几根，
// 其余休市时间（午休、收盘后、开盘前、周末、节假日）空闲；开盘后最近 3 根可以跨到上一交易日。
func TestMarketCanaryStockCN5mExpectedBars(t *testing.T) {
	requireTradingStatus(t, 2026, 8, 27, marketcalendar.TradingDay)
	requireTradingStatus(t, 2026, 8, 28, marketcalendar.TradingDay)
	requireTradingStatus(t, 2026, 8, 29, marketcalendar.NonTradingDay)
	requireTradingStatus(t, 2026, 10, 1, marketcalendar.NonTradingDay)
	cases := []struct {
		name string
		now  time.Time
		// want 为空表示空闲、不读存储。
		want []time.Time
	}{
		{"交易时段内", shanghai(2026, 8, 28, 10, 0, 10), []time.Time{shanghai(2026, 8, 28, 9, 45, 0), shanghai(2026, 8, 28, 9, 50, 0), shanghai(2026, 8, 28, 9, 55, 0)}},
		{"刚收盘还没等到写入", shanghai(2026, 8, 28, 10, 0, 3), []time.Time{shanghai(2026, 8, 28, 9, 40, 0), shanghai(2026, 8, 28, 9, 45, 0), shanghai(2026, 8, 28, 9, 50, 0)}},
		{"开盘后跨到上一交易日", shanghai(2026, 8, 28, 9, 35, 5), []time.Time{shanghai(2026, 8, 27, 14, 50, 0), shanghai(2026, 8, 27, 14, 55, 0), shanghai(2026, 8, 28, 9, 30, 0)}},
		{"开盘前", shanghai(2026, 8, 28, 9, 32, 0), nil},
		{"上午收盘后等待写入", shanghai(2026, 8, 28, 11, 30, 5), nil},
		{"上午收盘后检查最后几根", shanghai(2026, 8, 28, 11, 31, 5), []time.Time{shanghai(2026, 8, 28, 11, 15, 0), shanghai(2026, 8, 28, 11, 20, 0), shanghai(2026, 8, 28, 11, 25, 0)}},
		{"午休", shanghai(2026, 8, 28, 12, 0, 0), nil},
		{"下午第一根未收盘", shanghai(2026, 8, 28, 13, 3, 0), nil},
		{"下午第一根收盘后", shanghai(2026, 8, 28, 13, 5, 10), []time.Time{shanghai(2026, 8, 28, 11, 20, 0), shanghai(2026, 8, 28, 11, 25, 0), shanghai(2026, 8, 28, 13, 0, 0)}},
		{"收盘后等待写入", shanghai(2026, 8, 28, 15, 0, 30), nil},
		{"收盘后检查最后几根", shanghai(2026, 8, 28, 15, 1, 5), []time.Time{shanghai(2026, 8, 28, 14, 45, 0), shanghai(2026, 8, 28, 14, 50, 0), shanghai(2026, 8, 28, 14, 55, 0)}},
		{"收盘检查窗口已过", shanghai(2026, 8, 28, 15, 5, 0), nil},
		{"周末", shanghai(2026, 8, 29, 10, 0, 0), nil},
		{"节假日", shanghai(2026, 10, 1, 10, 0, 0), nil},
	}
	config := stockCanaryConfig("5m")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := make([]*storagepb.TimeSeriesRow, 0, len(tc.want))
			want := make([]string, 0, len(tc.want))
			for _, at := range tc.want {
				rows = append(rows, stockCanaryRow(at, "eastmoney"))
				want = append(want, at.UTC().Format(time.RFC3339Nano))
			}
			reader := &canaryReader{rows: rows}
			result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return tc.now }}).Run(t.Context())
			require.True(t, result.Success, result.ErrorMessage)
			if len(tc.want) == 0 {
				require.JSONEq(t, `{"state":"idle"}`, result.BodyExcerpt)
				require.Nil(t, reader.request)
				return
			}
			require.Empty(t, result.BodyExcerpt)
			require.Equal(t, want, requestedTimes(reader))
			require.Equal(t, "5m", reader.request.GetKeys()[0].GetTimeSeries().GetFreq())
		})
	}
}

func TestMarketCanaryStockCN5mReportsMissingBars(t *testing.T) {
	now := shanghai(2026, 8, 28, 15, 1, 5)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(shanghai(2026, 8, 28, 14, 45, 0), "eastmoney"),
		stockCanaryRow(shanghai(2026, 8, 28, 14, 50, 0), "tdx"),
	}}
	result := (MarketCanary{Reader: reader, Config: stockCanaryConfig("5m"), Now: func() time.Time { return now }}).Run(t.Context())
	require.Equal(t, "closed_bar_coverage_below_threshold", result.ErrorMessage)
}

func TestMarketCanaryStockCNRejectsPeriodThatDoesNotDivideSessions(t *testing.T) {
	reader := &canaryReader{}
	result := (MarketCanary{Reader: reader, Config: stockCanaryConfig("45m"), Now: func() time.Time {
		return shanghai(2026, 8, 28, 10, 0, 0)
	}}).Run(t.Context())
	require.Equal(t, "invalid_config", result.ErrorMessage)
	require.Nil(t, reader.request)
	require.Error(t, ValidateMarketCanary(stockCanaryConfig("45m")))
	require.NoError(t, ValidateMarketCanary(stockCanaryConfig("5m")))
	withoutProviders := stockCanaryConfig("5m")
	withoutProviders.EligibleKlineProviders = nil
	require.ErrorContains(t, ValidateMarketCanary(withoutProviders), "eligible_kline_providers")
}

func TestMarketCanaryStockCNReportsCalendarAndDataReasons(t *testing.T) {
	calendar, err := marketcalendar.Load("cn_stock")
	require.NoError(t, err)
	last := calendar.LastDate()
	config := stockCanaryConfig("1m")
	expired := shanghai(last.Year(), last.Month(), last.Day()+1, 10, 0, 0)
	result := (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time { return expired }}).Run(t.Context())
	require.Equal(t, "calendar_expired", result.ErrorMessage)
	expiring := shanghai(last.Year(), last.Month(), last.Day()-7, 10, 0, 0)
	result = (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time { return expiring }}).Run(t.Context())
	require.Equal(t, "calendar_expiring", result.ErrorMessage)

	now := shanghai(2026, 8, 28, 9, 33, 5)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(shanghai(2026, 8, 28, 9, 30, 0), ""),
		stockCanaryRow(shanghai(2026, 8, 28, 9, 31, 0), "sina"),
		stockCanaryRow(shanghai(2026, 8, 28, 9, 32, 0), "sina"),
	}}
	result = (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.Equal(t, "missing_source_provider", result.ErrorMessage)

	reader.rows = reader.rows[1:]
	result = (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.Equal(t, "closed_bar_coverage_below_threshold", result.ErrorMessage)
}

func TestMarketCanaryStockCNEnforcesConfiguredClosedBarCoverage(t *testing.T) {
	now := shanghai(2026, 8, 28, 9, 33, 5)
	config := stockCanaryConfig("1m")
	config.ClosedBarMinCoverage = 0.99
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(shanghai(2026, 8, 28, 9, 31, 0), "sina"),
		stockCanaryRow(shanghai(2026, 8, 28, 9, 32, 0), "tencent"),
	}}

	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.Equal(t, "closed_bar_coverage_below_threshold", result.ErrorMessage)
	require.JSONEq(t, `{"type":"closed_bar_coverage","expected":3,"actual":2,"coverage":0.6666666666666666,"minimum":0.99}`, result.BodyExcerpt)
}

func TestMarketCanaryStockCNReportsNoClosedBarData(t *testing.T) {
	now := shanghai(2026, 8, 28, 9, 33, 5)
	config := stockCanaryConfig("1m")

	result := (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.Equal(t, "no_closed_bar_data", result.ErrorMessage)
}

func TestMarketCanaryStockCNReportsNoEligibleKlineFeed(t *testing.T) {
	config := stockCanaryConfig("1m")
	config.EligibleKlineProviders = nil
	result := (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time {
		return shanghai(2026, 8, 28, 9, 33, 5)
	}}).Run(t.Context())
	require.Equal(t, "no_eligible_kline_feed", result.ErrorMessage)
}

// 空间 ID 即市场 ID：stockcn 空间的探针不配 market_id 也按 A 股交易时段判断。
func TestMarketCanaryStockCNMarketDefaultsToSpace(t *testing.T) {
	config := stockCanaryConfig("1m")
	config.MarketID = ""
	reader := &canaryReader{}
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return shanghai(2026, 8, 28, 12, 0, 0) }}).Run(t.Context())
	require.True(t, result.Success)
	require.JSONEq(t, `{"state":"idle"}`, result.BodyExcerpt)
}

func stockCanaryConfig(frequency string) MarketCanaryConfig {
	return MarketCanaryConfig{
		SpaceID: "stockcn", DatasetID: "dataset_stockcn_kline", SubjectID: "600000.XSHG", Frequency: frequency, SeriesTag: stringPtr("default"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.2, MarketID: "stockcn",
		SettleDelay: 5 * time.Second, PostCloseDelay: time.Minute, CalendarWarningLead: 14 * 24 * time.Hour, ClosedBarCount: 3, ClosedBarMinCoverage: 0.99,
		EligibleKlineProviders: []string{"sina", "tencent", "tdx", "eastmoney"},
	}
}

var shanghaiLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return location
}()

// shanghai 返回上海时间对应的 UTC 时刻。
func shanghai(year int, month time.Month, day, hour, minute, second int) time.Time {
	return time.Date(year, month, day, hour, minute, second, 0, shanghaiLocation).UTC()
}

func requireTradingStatus(t *testing.T, year int, month time.Month, day int, want marketcalendar.CoverageStatus) {
	t.Helper()
	calendar, err := marketcalendar.Load("cn_stock")
	require.NoError(t, err)
	date, err := marketcalendar.NewCivilDate(year, month, day)
	require.NoError(t, err)
	status, err := calendar.Status(date)
	require.NoError(t, err)
	require.Equal(t, want, status, "%s 的交易日状态", date)
}

func requestedTimes(reader *canaryReader) []string {
	out := make([]string, 0, len(reader.request.GetKeys()))
	for _, key := range reader.request.GetKeys() {
		out = append(out, key.GetTimeSeries().GetDataTime())
	}
	return out
}

func stockCanaryRow(at time.Time, provider string) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: "default"}, Fields: []*storagepb.FieldValue{
		{FieldId: "open", Value: doubleTyped(10)}, {FieldId: "high", Value: doubleTyped(11)},
		{FieldId: "low", Value: doubleTyped(9)}, {FieldId: "close", Value: doubleTyped(10.5)},
		{FieldId: "volume", Value: doubleTyped(100)}, {FieldId: "amount", Value: doubleTyped(1000)},
		{FieldId: "source_provider", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: provider}}},
	}}
}

func doubleTyped(value float64) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}
}

func marketCanaryRow(at time.Time, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return marketCanaryTaggedRow(at, "venue:binance", closeValue, volumeValue)
}

func marketCanaryTaggedRow(at time.Time, seriesTag string, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag},
		Fields: []*storagepb.FieldValue{
			{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}
