package observability

import (
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func arrivalRow(producer, spaceID, datasetID, primaryID string, bar, arrived time.Time, previous *time.Time) monmetrics.BarArrival {
	return monmetrics.BarArrival{
		Producer: producer, SpaceID: spaceID, DatasetID: datasetID, PrimaryDatasetID: primaryID, Freq: "1m",
		BarAt: bar.UTC(), PreviousBarAt: previous, ArrivedAt: arrived.UTC(), ReportedAt: arrived.UTC(),
	}
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func TestArrivalHistoryGroupsTheChainByStageInUTCDays(t *testing.T) {
	day := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		rows := []monmetrics.BarArrival{
			// 前一天 23:59 那根在 10-16 00:00 收线：算 10-16 的。
			arrivalRow("storage", "crypto", "dataset_spot_1m", "", day.Add(-time.Minute), day.Add(time.Second), nil),
			arrivalRow("storage", "crypto", "dataset_spot_1m", "", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute+time.Second), nil),
			// 补数据：上一份快照停在 18:54，这次一批写到 18:58，中间 3 根不单独记录。
			arrivalRow("storage", "crypto", "dataset_spot_1m", "", day.Add(18*time.Hour+58*time.Minute), day.Add(18*time.Hour+59*time.Minute+40*time.Second), timePtr(day.Add(18*time.Hour+54*time.Minute))),
			// 10-16 23:59 那根在 10-17 收线：不算这一天。
			arrivalRow("storage", "crypto", "dataset_spot_1m", "", day.Add(23*time.Hour+59*time.Minute), day.Add(24*time.Hour+time.Second), nil),
			arrivalRow("storage_view", "crypto", "view_spot_kline_1m", "dataset_spot_1m", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute+3*time.Second), nil),
			arrivalRow("factor", "crypto", "dataset_factor_spot_1m", "", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute+9*time.Second), nil),
			arrivalRow("storage_view", "crypto", "view_factor_spot_1m", "dataset_factor_spot_1m", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute+11*time.Second), nil),
			arrivalRow("strategy", "crypto", "strategy_abc", "", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute+15*time.Second), nil),
			// 别的链路不出现。
			arrivalRow("storage", "crypto", "dataset_swap_1m", "", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute), nil),
			arrivalRow("storage_view", "crypto", "view_swap_kline_1m", "dataset_swap_1m", day.Add(18*time.Hour+54*time.Minute), day.Add(18*time.Hour+55*time.Minute), nil),
		}
		require.NoError(t, db.Create(&rows).Error)
	})
	policy := testRealtimePolicy()
	policy.Defaults.MinuteLateness = 30 * time.Second
	builder := Builder{Metrics: query, Inventory: cryptoInventory, Policy: policy, Now: func() time.Time { return day.Add(20 * time.Hour) }}

	got, err := builder.ArrivalHistory(t.Context(), ArrivalHistoryQuery{SpaceID: "crypto", DatasetID: "dataset_spot_1m", Freq: "1m"})
	require.NoError(t, err)
	require.Equal(t, "UTC", got.Timezone)
	require.Equal(t, "2026-10-16", got.Day, "不传日期取市场时区的今天")
	require.True(t, got.Start.Equal(day))
	require.True(t, got.End.Equal(day.AddDate(0, 0, 1)))
	require.False(t, got.RetentionTruncated)
	var stages []string
	for _, series := range got.Series {
		stages = append(stages, series.Stage+":"+series.DatasetID)
		require.EqualValues(t, 30, series.LatenessLimitSeconds)
	}
	require.Equal(t, []string{"storage:dataset_spot_1m", "view:view_spot_kline_1m", "factor:dataset_factor_spot_1m", "factor_view:view_factor_spot_1m", "strategy:strategy_abc"}, stages)

	storage := got.Series[0].Points
	require.Len(t, storage, 3)
	require.True(t, storage[0].Close.Equal(day))
	require.EqualValues(t, 1, storage[0].DelaySeconds)
	require.True(t, storage[1].Close.Equal(day.Add(18*time.Hour+55*time.Minute)))
	require.EqualValues(t, 1, storage[1].DelaySeconds)
	require.Zero(t, storage[1].SkippedBars)
	require.True(t, storage[2].Close.Equal(day.Add(18*time.Hour+59*time.Minute)))
	require.EqualValues(t, 40, storage[2].DelaySeconds)
	require.Equal(t, 3, storage[2].SkippedBars)
	require.EqualValues(t, 11, got.Series[3].Points[0].DelaySeconds)

	// 8 天前超出 7 天的保留期，标出可能不完整。
	old, err := builder.ArrivalHistory(t.Context(), ArrivalHistoryQuery{SpaceID: "crypto", DatasetID: "dataset_spot_1m", Freq: "1m", Day: "2026-10-08"})
	require.NoError(t, err)
	require.True(t, old.RetentionTruncated)
	require.Empty(t, old.Series)
}

func TestArrivalHistoryUsesBeijingDaysForAShares(t *testing.T) {
	// 2026-10-16 北京时间是周五；前一天 15:00 收盘，这一天 09:30 开盘。
	friday := cst("2026-10-16", "09:30:00")
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		rows := []monmetrics.BarArrival{
			arrivalRow("storage", "stockcn", "dataset_stockcn_kline_1m", "", cst("2026-10-15", "14:59:00"), cst("2026-10-15", "15:00:04"), nil),
			// 隔夜之后的第一根：上一份快照停在前一天 14:59，中间没有交易时段，不算跳过。
			arrivalRow("storage", "stockcn", "dataset_stockcn_kline_1m", "", friday, cst("2026-10-16", "09:31:02"), timePtr(cst("2026-10-15", "14:59:00"))),
		}
		require.NoError(t, db.Create(&rows).Error)
	})
	inventory := staticInventory{{SpaceID: "stockcn", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}}}
	builder := Builder{Metrics: query, Inventory: inventory, Policy: testRealtimePolicy(), Now: func() time.Time { return cst("2026-10-16", "12:00:00") }}

	got, err := builder.ArrivalHistory(t.Context(), ArrivalHistoryQuery{SpaceID: "stockcn", DatasetID: "dataset_stockcn_kline_1m", Freq: "1m", Day: "2026-10-16"})
	require.NoError(t, err)
	require.Equal(t, "Asia/Shanghai", got.Timezone)
	require.True(t, got.Start.Equal(cst("2026-10-16", "00:00:00")), "按北京时间的自然日切分")
	require.Len(t, got.Series, 1)
	require.Len(t, got.Series[0].Points, 1, "前一天 15:00 收线的那根不算这一天")
	point := got.Series[0].Points[0]
	require.True(t, point.Close.Equal(cst("2026-10-16", "09:31:00")))
	require.EqualValues(t, 2, point.DelaySeconds)
	require.Zero(t, point.SkippedBars)
}

func TestArrivalHistoryRejectsInvalidQueries(t *testing.T) {
	query := openOverviewMetrics(t, func(*gorm.DB) {})
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	builder := Builder{Metrics: query, Inventory: cryptoInventory, Policy: testRealtimePolicy(), Now: func() time.Time { return now }}
	for _, q := range []ArrivalHistoryQuery{
		{SpaceID: "crypto", Freq: "1m"},
		{SpaceID: "crypto", DatasetID: "dataset_spot_1m", Freq: "7x"},
		{SpaceID: "crypto", DatasetID: "dataset_spot_1m", Freq: "1m", Day: "2026/10/16"},
		{SpaceID: "crypto", DatasetID: "dataset_spot_1m", Freq: "1m", Day: "2026-10-17"},
		// 没有交易日历的系统数据不记录到达。
		{SpaceID: "mooxsys", DatasetID: "dataset_mooxsys_host_resource", Freq: "1m"},
	} {
		_, err := builder.ArrivalHistory(t.Context(), q)
		require.ErrorIs(t, err, ErrInvalidArrivalQuery, "%+v", q)
	}
}

func TestDatasetStatusCarriesTheMarketTimezone(t *testing.T) {
	now := time.Date(2026, 10, 16, 6, 10, 30, 0, time.UTC)
	key := datasetKey{producer: "storage", spaceID: "crypto", datasetID: "dataset_spot_1m", freq: "1m"}
	require.Equal(t, "UTC", datasetStatus(now, key, healthyRun(now, now.Add(-2*time.Minute), now), testRealtimePolicy(), cryptoClock).Timezone)
	stock := datasetStatus(cst("2026-10-16", "10:00:00"), stockKey, healthyRun(cst("2026-10-16", "10:00:00"), cst("2026-10-16", "09:58:00"), cst("2026-10-16", "09:59:01")), testRealtimePolicy(), stockSpaceClock(t, stockDays))
	require.Equal(t, "Asia/Shanghai", stock.Timezone)
	system := datasetStatus(now, key, healthyRun(now, now.Add(-time.Minute), now), testRealtimePolicy(), spaceClock{err: monmetrics.ErrNoTradingCalendar})
	require.Equal(t, "UTC", system.Timezone)
}

func TestArrivalHistoryFindsWeeklyBarsOpenedWeeksBefore(t *testing.T) {
	// 加密货币周线 10-12（周一）开盘、10-19 收线：查 10-19 这一天要往前读到 10-12。
	weekOpen := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		row := arrivalRow("storage", "crypto", "dataset_spot_1w", "", weekOpen, weekOpen.AddDate(0, 0, 7).Add(5*time.Second), nil)
		row.Freq = "1w"
		require.NoError(t, db.Create(&row).Error)
	})
	builder := Builder{Metrics: query, Inventory: cryptoInventory, Policy: testRealtimePolicy(), Now: func() time.Time { return weekOpen.AddDate(0, 0, 7).Add(time.Hour) }}
	got, err := builder.ArrivalHistory(t.Context(), ArrivalHistoryQuery{SpaceID: "crypto", DatasetID: "dataset_spot_1w", Freq: "1w", Day: "2026-10-19"})
	require.NoError(t, err)
	require.Len(t, got.Series, 1)
	require.Len(t, got.Series[0].Points, 1)
	require.True(t, got.Series[0].Points[0].Close.Equal(weekOpen.AddDate(0, 0, 7)))
	require.EqualValues(t, 5, got.Series[0].Points[0].DelaySeconds)
}
