package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// A 股日线计划：交易日 D 的日线在 D+1 日 00:00 UTC（北京时间 08:00）到期，周期与取数目标都是 D 的日期键；
// 其他整点不到期。
func TestPlanBuildRequestsStockDailyIsDueAtNextUTCMidnight(t *testing.T) {
	plan := &Plan{
		V:      1,
		Market: PlanMarket{MarketID: "stockcn", InstrumentType: "equity", Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity"},
		Fetch:  PlanFetch{Concurrency: 8, RPS: 20, Burst: 20, FeedConcurrency: 8},
		Datasets: []PlanDataset{{DatasetID: "dataset_stock_1d", Frequency: "1d", LatenessS: 90, Copies: []PlanCopy{
			{From: 1000, Hash: "h", Count: 1, Subjects: []PlanSubject{{ID: "600519.XSHG", Index: 0}}},
		}}},
	}
	key := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	requests, err := plan.BuildRequests("stockcn", "fn", "req", key.Add(24*time.Hour))
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, key, requests[0].Period)
	require.Equal(t, key.Add(24*time.Hour+90*time.Second), requests[0].StorageDeadline)
	item := requests[0].Request.Items[0]
	require.Equal(t, key.Format(time.RFC3339), item.TargetDataTime)
	require.Equal(t, "1d", item.Frequency)
	require.True(t, item.RequirePeriodCommit)

	for _, hour := range []int{1, 7, 8, 16} {
		requests, err = plan.BuildRequests("stockcn", "fn", "req", key.Add(time.Duration(hour)*time.Hour))
		require.NoError(t, err)
		require.Empty(t, requests, "%d 点不到期", hour)
	}
}

// 没有周期提交的实时批：此刻对应的上一根日线是交易日才取数。
func TestLatestClosedIsTradingBarStockDaily(t *testing.T) {
	for at, want := range map[time.Time]bool{
		time.Date(2026, 11, 11, 0, 0, 5, 0, time.UTC): true,  // 上一根是 11-10（周二）
		time.Date(2026, 11, 9, 0, 0, 5, 0, time.UTC):  false, // 上一根是 11-08（周日）
		time.Date(2026, 11, 7, 0, 0, 5, 0, time.UTC):  true,  // 周六 08:00，上一根是 11-06（周五）
	} {
		got, err := latestClosedIsTradingBar(StockCNSpaceID, marketdata.FrequencyDay, at, 0)
		require.NoError(t, err)
		require.Equal(t, want, got, at)
	}
}

// A 股日线的行：频率 1d，行键是交易日期的 UTC 零点，trade_date 是同一个交易日，close_time 是键加 24 小时。
func TestStockKlineRowWritesDailyBarAtTheDateKey(t *testing.T) {
	key := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600519.XSHG", ProviderID: "tdx", SourceID: "normal_7709", ProviderSymbol: "sh600519", Frequency: "1d",
		BarStart: key, BarEnd: key.Add(24 * time.Hour), Open: 1438.23, High: 1444.5, Low: 1431.26, Close: 1439,
		Volume: 2303784, Amount: 3316078848,
		ProviderTimestamp: key.Add(7 * time.Hour), FetchedAt: key.Add(24 * time.Hour), RequestID: "req",
	}
	row, err := stockKlineRow(bar, testStockDatasetID, StockCNRouteID("1d"), 1, "贵州茅台")
	require.NoError(t, err)
	series := row.GetKey().GetTimeSeries()
	require.Equal(t, "1d", series.GetFreq())
	require.Equal(t, "2026-11-10T00:00:00Z", series.GetDataTime())
	fields := map[string]*storagepb.FieldValue{}
	for _, field := range row.GetFields() {
		fields[field.GetFieldId()] = field
	}
	require.Equal(t, 1439.0, fields["close"].GetValue().GetDoubleValue())
	require.Equal(t, 2303784.0, fields["volume"].GetValue().GetDoubleValue())
	require.Equal(t, "stockcn_equity_kline_1d", fields["route_id"].GetValue().GetStringValue())
	require.Equal(t, "2026-11-09T16:00:00Z", fields["trade_date"].GetValue().GetTimeValue(), "trade_date 是交易日 11-10 的上海零点，与日内行一致")
	require.Equal(t, "2026-11-11T00:00:00Z", fields["close_time"].GetValue().GetTimeValue())

	// 上海零点的键过不了统一校验。
	bar.BarStart, bar.BarEnd = key.Add(-8*time.Hour), key.Add(16*time.Hour)
	_, err = stockKlineRow(bar, testStockDatasetID, StockCNRouteID("1d"), 1, "贵州茅台")
	require.ErrorContains(t, err, "UTC 1d bucket")
}
