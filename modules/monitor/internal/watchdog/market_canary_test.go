package watchdog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	wanted := make(map[string]struct{}, len(request.GetKeys()))
	for _, key := range request.GetKeys() {
		wanted[key.GetDataTime()+"\n"+key.GetSeriesTag()] = struct{}{}
	}
	rows := make([]*storagepb.TimeSeriesRow, 0, len(r.rows))
	for _, row := range r.rows {
		if _, ok := wanted[row.GetKey().GetDataTime()+"\n"+row.GetKey().GetSeriesTag()]; ok {
			rows = append(rows, row)
		}
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
	wanted := make(map[string]struct{}, len(request.GetKeys()))
	for _, key := range request.GetKeys() {
		wanted[key.GetDataTime()] = struct{}{}
	}
	rows := make([]*storagepb.TimeSeriesRow, 0, len(r.rows))
	for _, row := range r.rows {
		if _, ok := wanted[row.GetKey().GetDataTime()]; ok {
			rows = append(rows, row)
		}
	}
	return &storagepb.ReadTimeSeriesRowsRsp{
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
	require.Equal(t, "crypto", reader.request.GetSpaceId())
	require.Equal(t, "market_kline", reader.request.GetDatasetId())
	require.Equal(t, "BTC-USDT", reader.request.GetKeys()[0].GetSubjectId())
	require.Equal(t, "1m", reader.request.GetKeys()[0].GetFreq())
	require.Equal(t, "venue:binance", reader.request.GetKeys()[0].GetSeriesTag())
	require.Len(t, reader.request.GetKeys(), 24)
	require.Equal(t, now.Format(time.RFC3339Nano), reader.request.GetKeys()[0].GetDataTime())
	require.Equal(t, now.Add(-23*time.Minute).Format(time.RFC3339Nano), reader.request.GetKeys()[23].GetDataTime())
	require.Equal(t, []string{"close"}, reader.request.GetColumnNames())
	require.Nil(t, reader.request.GetTimeRange())
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
	require.Equal(t, "venue:binance", reader.request.GetKeys()[0].GetSeriesTag())
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
	now := time.Date(2026, 8, 28, 1, 33, 5, 0, time.UTC) // 09:33:05 Asia/Shanghai.
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(time.Date(2026, 8, 28, 1, 30, 0, 0, time.UTC), "sina"),
		stockCanaryRow(time.Date(2026, 8, 28, 1, 31, 0, 0, time.UTC), "tencent"),
		stockCanaryRow(time.Date(2026, 8, 28, 1, 32, 0, 0, time.UTC), "eastmoney"),
	}}
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.True(t, result.Success)
	require.Equal(t, []string{"open", "high", "low", "close", "volume", "amount", "source_provider"}, reader.request.GetColumnNames())
	require.Len(t, reader.request.GetKeys(), 3)
	require.Equal(t, "default", reader.request.GetKeys()[0].GetSeriesTag())
}

func TestMarketCanaryStockCNChecksFinalBarDuringPostCloseGrace(t *testing.T) {
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	early := time.Date(2026, 8, 28, 7, 0, 5, 0, time.UTC) // 15:00:05 Asia/Shanghai.
	earlyReader := &canaryReader{}
	earlyResult := (MarketCanary{Reader: earlyReader, Config: config, Now: func() time.Time { return early }}).Run(t.Context())
	require.True(t, earlyResult.Success)
	require.JSONEq(t, `{"state":"idle"}`, earlyResult.BodyExcerpt)
	require.Nil(t, earlyReader.request)

	now := time.Date(2026, 8, 28, 7, 1, 5, 0, time.UTC) // 15:01:05 Asia/Shanghai.
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(time.Date(2026, 8, 28, 6, 57, 0, 0, time.UTC), "sina"),
		stockCanaryRow(time.Date(2026, 8, 28, 6, 58, 0, 0, time.UTC), "sina"),
		stockCanaryRow(time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), "sina"),
	}}
	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.True(t, result.Success)
	require.NotNil(t, reader.request)
	foundFinalBar := false
	for _, key := range reader.request.GetKeys() {
		if key.GetDataTime() == "2026-08-28T06:59:00Z" && key.GetSeriesTag() == "default" {
			foundFinalBar = true
			break
		}
	}
	require.True(t, foundFinalBar)
}

func TestMarketCanaryStockCNMiddayAndClosedDayAreIdleHealthy(t *testing.T) {
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	for _, now := range []time.Time{
		time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC), // 12:00 lunch.
		time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC), // National Day closure.
	} {
		reader := &canaryReader{}
		result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
		require.True(t, result.Success)
		require.JSONEq(t, `{"state":"idle"}`, result.BodyExcerpt)
		require.Nil(t, reader.request)
	}
}

func TestMarketCanaryStockCNReportsCalendarAndDataReasons(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 33, 5, 0, time.UTC)
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-08-27"))
	result := (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.Equal(t, "calendar_expired", result.ErrorMessage)

	config = stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(time.Date(2026, 8, 28, 1, 30, 0, 0, time.UTC), ""),
		stockCanaryRow(time.Date(2026, 8, 28, 1, 31, 0, 0, time.UTC), "sina"),
		stockCanaryRow(time.Date(2026, 8, 28, 1, 32, 0, 0, time.UTC), "sina"),
	}}
	result = (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.Equal(t, "missing_source_provider", result.ErrorMessage)

	reader.rows = reader.rows[1:]
	result = (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.Equal(t, "closed_bar_coverage_below_threshold", result.ErrorMessage)
}

func TestMarketCanaryStockCNEnforcesConfiguredClosedBarCoverage(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 33, 5, 0, time.UTC)
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	config.ClosedBarMinCoverage = 0.99
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		stockCanaryRow(time.Date(2026, 8, 28, 1, 31, 0, 0, time.UTC), "sina"),
		stockCanaryRow(time.Date(2026, 8, 28, 1, 32, 0, 0, time.UTC), "tencent"),
	}}

	result := (MarketCanary{Reader: reader, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.Equal(t, "closed_bar_coverage_below_threshold", result.ErrorMessage)
	require.JSONEq(t, `{"type":"stockcn_closed_bar_coverage","expected":3,"actual":2,"coverage":0.6666666666666666,"minimum":0.99}`, result.BodyExcerpt)
}

func TestMarketCanaryStockCNReportsNoClosedBarData(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 33, 5, 0, time.UTC)
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	config.ClosedBarMinCoverage = 0.99

	result := (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time { return now }}).Run(t.Context())

	require.Equal(t, "no_closed_bar_data", result.ErrorMessage)
}

func TestMarketCanaryStockCNReportsNoEligibleKlineFeed(t *testing.T) {
	config := stockCanaryConfig(t, stockCalendarPath(t, "2026-12-31"))
	config.EligibleKlineProviders = nil
	result := (MarketCanary{Reader: &canaryReader{}, Config: config, Now: func() time.Time {
		return time.Date(2026, 8, 28, 1, 33, 5, 0, time.UTC)
	}}).Run(t.Context())
	require.Equal(t, "no_eligible_kline_feed", result.ErrorMessage)
}

func stockCanaryConfig(t *testing.T, calendarPath string) MarketCanaryConfig {
	t.Helper()
	return MarketCanaryConfig{
		SpaceID: "stockcn", DatasetID: "dataset_stockcn_equity_kline", SubjectID: "600000.XSHG", Frequency: "1m", SeriesTag: stringPtr("default"),
		Freshness: 3 * time.Minute, ReturnThreshold: 0.2, MarketID: "stockcn", CalendarPath: calendarPath,
		SettleDelay: 5 * time.Second, PostCloseDelay: time.Minute, CalendarWarningLead: 14 * 24 * time.Hour, ClosedBarCount: 3, ClosedBarMinCoverage: 0.99,
		EligibleKlineProviders: []string{"sina", "tencent", "tdx", "eastmoney"},
	}
}

func stockCalendarPath(t *testing.T, coverageEnd string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "calendar.yaml")
	raw := "timezone: Asia/Shanghai\ncoverage_start: \"2026-01-01\"\ncoverage_end: \"" + coverageEnd + "\"\nsessions:\n  - start: \"09:30\"\n    end: \"11:30\"\n  - start: \"13:00\"\n    end: \"15:00\"\nclosed_dates:\n  - \"2026-10-01\"\nexceptional_open_dates: []\n"
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	return path
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
			{FieldId: "market_kline.close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "market_kline.volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}
