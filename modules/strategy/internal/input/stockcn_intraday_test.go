package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// shAt 返回上海时间 2026 年的某一时刻。
func shAt(t *testing.T, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	return time.Date(2026, month, day, hour, minute, 0, 0, shanghai(t)).UTC()
}

func mustPeriod(t *testing.T, compute func() (PeriodBoundaries, error)) PeriodBoundaries {
	t.Helper()
	period, err := compute()
	if err != nil {
		t.Fatal(err)
	}
	return period
}

func sameTime(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s：期望 %s，实际 %s", name, want.In(shanghai(t)), got.In(shanghai(t)))
	}
}

// A 股 5m 跨午休：11:25 那根之后是 13:00 那根，有效期（往后 ValidBars=2 根）到 13:10。
func TestStockIntradayAcrossLunchBreak(t *testing.T) {
	morning := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", shAt(t, 10, 8, 11, 25)) })
	sameTime(t, "11:25 的 bar_end", morning.BarEnd, shAt(t, 10, 8, 11, 30))
	sameTime(t, "11:25 的上一根", morning.PreviousStart, shAt(t, 10, 8, 11, 20))
	sameTime(t, "11:25 的下一根 bar_end", morning.NextEnd, shAt(t, 10, 8, 13, 5))
	valid, err := AdvanceBarEnd("cn_stock", "5m", morning.BarEnd, 2)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "11:25 的有效期", valid, shAt(t, 10, 8, 13, 10))
	back, err := AdvanceBarEnd("cn_stock", "5m", shAt(t, 10, 8, 13, 5), -1)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "13:00 那根往前一根", back, shAt(t, 10, 8, 11, 30))

	afternoon := mustPeriod(t, func() (PeriodBoundaries, error) { return FromBarEnd("cn_stock", "5m", shAt(t, 10, 8, 13, 5)) })
	sameTime(t, "13:05 的 bar_start", afternoon.StorageStart, shAt(t, 10, 8, 13, 0))
	sameTime(t, "13:00 的上一根", afternoon.PreviousStart, shAt(t, 10, 8, 11, 25))
	if afternoon.BarIndex != morning.BarIndex+1 {
		t.Fatalf("午休不占根数：%d → %d", morning.BarIndex, afternoon.BarIndex)
	}

	for name, tc := range map[string]struct{ trigger, start time.Time }{
		"午休中":      {shAt(t, 10, 8, 12, 0), shAt(t, 10, 8, 11, 25)},
		"13:04":    {shAt(t, 10, 8, 13, 4), shAt(t, 10, 8, 11, 25)},
		"13:05 收盘": {shAt(t, 10, 8, 13, 5), shAt(t, 10, 8, 13, 0)},
		"盘中":       {shAt(t, 10, 8, 10, 2), shAt(t, 10, 8, 9, 55)},
	} {
		closed := mustPeriod(t, func() (PeriodBoundaries, error) { return ClosedPeriod("cn_stock", "5m", tc.trigger) })
		sameTime(t, name+"最近闭合的一根", closed.StorageStart, tc.start)
	}

	// 历史回看跨午休：以 13:00 为最后一根共 7 根，第一根是 11:00。
	first, err := HistoryStart("cn_stock", "5m", shAt(t, 10, 8, 13, 0), 7)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "跨午休的回看起点", first, shAt(t, 10, 8, 11, 0))
	previous, err := PreviousStorageStart("cn_stock", "5m", shAt(t, 10, 8, 13, 0))
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "13:00 的上一根 bar_start", previous, shAt(t, 10, 8, 11, 25))
}

// 跨周末与国庆：2026-09-30（周三）14:55 那根之后是 10-08（周四）09:30 那根；周五 15:00 往后一根是周一 09:35。
func TestStockIntradayAcrossWeekendAndHoliday(t *testing.T) {
	last := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", shAt(t, 9, 30, 14, 55)) })
	sameTime(t, "节前最后一根的 bar_end", last.BarEnd, shAt(t, 9, 30, 15, 0))
	sameTime(t, "节前最后一根的下一根", last.NextEnd, shAt(t, 10, 8, 9, 35))
	first := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", shAt(t, 10, 8, 9, 30)) })
	sameTime(t, "节后第一根的上一根", first.PreviousStart, shAt(t, 9, 30, 14, 55))
	if first.BarIndex != last.BarIndex+1 {
		t.Fatalf("长假不占根数：%d → %d", last.BarIndex, first.BarIndex)
	}
	next, err := AdvanceBarEnd("cn_stock", "5m", last.BarEnd, 1)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "节前最后一根往后一根", next, shAt(t, 10, 8, 9, 35))
	for name, tc := range map[string]struct{ trigger, start time.Time }{
		"假期中":         {shAt(t, 10, 5, 10, 0), shAt(t, 9, 30, 14, 55)},
		"节后 09:34":    {shAt(t, 10, 8, 9, 34), shAt(t, 9, 30, 14, 55)},
		"节后 09:35":    {shAt(t, 10, 8, 9, 35), shAt(t, 10, 8, 9, 30)},
		"周六":          {shAt(t, 10, 10, 10, 0), shAt(t, 10, 9, 14, 55)},
		"周一开盘前 09:00": {shAt(t, 10, 12, 9, 0), shAt(t, 10, 9, 14, 55)},
	} {
		closed := mustPeriod(t, func() (PeriodBoundaries, error) { return ClosedPeriod("cn_stock", "5m", tc.trigger) })
		sameTime(t, name+"最近闭合的一根", closed.StorageStart, tc.start)
	}
	monday, err := AdvanceBarEnd("cn_stock", "5m", shAt(t, 10, 9, 15, 0), 1)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "周五收盘往后一根", monday, shAt(t, 10, 12, 9, 35))
	start, err := HistoryStart("cn_stock", "5m", shAt(t, 10, 8, 9, 30), 3)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "跨国庆的回看起点（10-08 09:30、09-30 14:55、14:50）", start, shAt(t, 9, 30, 14, 50))
}

// BarIndex 是 barclock 的交易序号：相邻两根相差 1，一个交易日 48 根，与因子的交易序号轴一致。
func TestStockIntradayBarIndexMatchesBarclock(t *testing.T) {
	clock, err := barclock.ClockFor("stockcn", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []time.Time{shAt(t, 10, 8, 9, 30), shAt(t, 10, 8, 11, 25), shAt(t, 10, 8, 13, 0), shAt(t, 10, 8, 14, 55)} {
		period := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", start) })
		bucket, ok, err := clock.BucketAt(start)
		if err != nil || !ok {
			t.Fatalf("barclock 找不到 %s 的桶：%v", start, err)
		}
		want, err := clock.Ordinal(bucket)
		if err != nil || period.BarIndex != want {
			t.Fatalf("%s 的 BarIndex 应等于 barclock 交易序号 %d：%d err=%v", start, want, period.BarIndex, err)
		}
	}
	dayClose := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", shAt(t, 10, 8, 14, 55)) })
	open := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", shAt(t, 10, 9, 9, 30)) })
	if open.BarIndex != dayClose.BarIndex+1 {
		t.Fatalf("隔夜不占根数：%d → %d", dayClose.BarIndex, open.BarIndex)
	}
	day := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "5m", shAt(t, 10, 8, 9, 30)) })
	if open.BarIndex-day.BarIndex != 48 {
		t.Fatalf("一个交易日应是 48 根：%d", open.BarIndex-day.BarIndex)
	}
}

// 不在交易时段 bar 起点上的行键、越过日历首尾的推进都报错，越界时用“之前 / 之后没有交易日”的哨兵错误。
func TestStockIntradayRejectsOffGridAndOutOfCalendar(t *testing.T) {
	for name, at := range map[string]time.Time{
		"不是 5 分钟起点": shAt(t, 10, 8, 9, 31),
		"午休":        shAt(t, 10, 8, 12, 0),
		"假期":        shAt(t, 10, 5, 10, 0),
		"开盘前":       shAt(t, 10, 8, 9, 0),
	} {
		if _, err := FromStorageStart("cn_stock", "5m", at); err == nil {
			t.Fatalf("%s 的行键应拒绝", name)
		}
	}
	if _, err := FromBarEnd("cn_stock", "5m", shAt(t, 10, 8, 13, 0)); err == nil || !strings.Contains(err.Error(), "终点") {
		t.Fatalf("13:00 不是任何一根的 bar_end：%v", err)
	}
	if _, err := AdvanceBarEnd("cn_stock", "5m", time.Date(2027, 12, 31, 15, 0, 0, 0, shanghai(t)).UTC(), 1); !errors.Is(err, marketcalendar.ErrNoNextTradingDay) {
		t.Fatalf("越过日历末尾应报之后没有交易日：%v", err)
	}
	if _, err := HistoryStart("cn_stock", "5m", shAt(t, 10, 8, 9, 30), 1_000_000_000); !errors.Is(err, marketcalendar.ErrNoPreviousTradingDay) {
		t.Fatalf("追溯到日历起点之前应报之前没有交易日：%v", err)
	}
	if err := CheckCalendar("cn_stock", "5m"); err != nil {
		t.Fatalf("A 股支持 5m：%v", err)
	}
	if err := CheckCalendar("cn_stock", "7m"); !errors.Is(err, ErrUnsupportedCalendar) {
		t.Fatalf("A 股不支持 7m：%v", err)
	}
}

// 覆盖范围的规整：最早一根取不早于它的交易时段 bar、最晚一根取不晚于它的，超出日历时夹到日历最后一根。
func TestStockIntradayFloorAndCeil(t *testing.T) {
	for name, tc := range map[string]struct{ at, want time.Time }{
		"盘中":   {shAt(t, 10, 8, 10, 2), shAt(t, 10, 8, 10, 0)},
		"起点":   {shAt(t, 10, 8, 10, 0), shAt(t, 10, 8, 10, 0)},
		"午休":   {shAt(t, 10, 8, 12, 0), shAt(t, 10, 8, 11, 25)},
		"假期":   {shAt(t, 10, 5, 10, 0), shAt(t, 9, 30, 14, 55)},
		"日历之外": {time.Date(2028, 1, 5, 10, 0, 0, 0, shanghai(t)), time.Date(2027, 12, 31, 14, 55, 0, 0, shanghai(t)).UTC()},
	} {
		got, err := FloorStorageStart("cn_stock", "5m", tc.at)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		sameTime(t, name+"向下规整", got, tc.want)
	}
	for name, tc := range map[string]struct{ at, want time.Time }{
		"盘中":    {shAt(t, 10, 8, 10, 2), shAt(t, 10, 8, 10, 5)},
		"起点":    {shAt(t, 10, 8, 10, 0), shAt(t, 10, 8, 10, 0)},
		"午休":    {shAt(t, 10, 8, 12, 0), shAt(t, 10, 8, 13, 0)},
		"节前收盘后": {shAt(t, 9, 30, 15, 30), shAt(t, 10, 8, 9, 30)},
	} {
		got, err := CeilStorageStart("cn_stock", "5m", tc.at)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		sameTime(t, name+"向上规整", got, tc.want)
	}
}

// 回放枚举跨午休：上海 11:00 至 13:30 共 12 根，逐根相邻。
func TestStockIntradayReplayBarsAcrossLunch(t *testing.T) {
	start, end, err := ReplayRange("cn_stock", "5m", shAt(t, 10, 8, 11, 0), shAt(t, 10, 8, 13, 30))
	if err != nil {
		t.Fatal(err)
	}
	bars, err := ReplayBars("cn_stock", "5m", start, end, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 12 {
		t.Fatalf("应有 12 根：%d", len(bars))
	}
	sameTime(t, "第一根", bars[0].StorageStart, shAt(t, 10, 8, 11, 0))
	sameTime(t, "午休前最后一根", bars[5].StorageStart, shAt(t, 10, 8, 11, 25))
	sameTime(t, "午休后第一根", bars[6].StorageStart, shAt(t, 10, 8, 13, 0))
	for i := 1; i < len(bars); i++ {
		if bars[i].BarIndex != bars[i-1].BarIndex+1 {
			t.Fatalf("第 %d 根的 BarIndex 不相邻：%d → %d", i, bars[i-1].BarIndex, bars[i].BarIndex)
		}
	}
	// 起点落在午休里时从 13:00 那根开始。
	bars, err = ReplayBars("cn_stock", "5m", shAt(t, 10, 8, 12, 0), shAt(t, 10, 8, 13, 10), 100)
	if err != nil || len(bars) != 2 || !bars[0].StorageStart.Equal(shAt(t, 10, 8, 13, 0)) {
		t.Fatalf("起点在午休时应从 13:00 开始：%+v err=%v", bars, err)
	}
	if label := PeriodLabel("cn_stock", "5m", shAt(t, 10, 8, 13, 0)); label != "2026-10-08 13:00（上海时间）" {
		t.Fatalf("日内的周期标识写上海时间：%s", label)
	}
}

// 每个交易日的根数与保留根数：5m 一天 48 根，300 天保留期约 200 个交易日、9600 根；日线仍是 200 根。
func TestStockIntradayTradingDayBarsAndRetention(t *testing.T) {
	for _, tc := range []struct {
		calendar, bar string
		want          int
	}{{"cn_stock", "5m", 48}, {"cn_stock", "1h", 4}, {"cn_stock", "1d", 1}, {DefaultCalendar, "5m", 0}, {"cn_stock", "7m", 0}} {
		if got := TradingDayBars(tc.calendar, tc.bar); got != tc.want {
			t.Fatalf("%s/%s 每个交易日应有 %d 根：%d", tc.calendar, tc.bar, tc.want, got)
		}
	}
	if bars, ok := retentionBars("7200h", "cn_stock", "5m"); !ok || bars != 9600 {
		t.Fatalf("A 股 5m 的保留根数应按每日 48 根折算：%d、%v", bars, ok)
	}
	if bars, ok := retentionBars("7200h", "cn_stock", "1d"); !ok || bars != 200 {
		t.Fatalf("A 股日线的保留根数不变：%d、%v", bars, ok)
	}
}

// A 股（market_type=equity）按现货处理：不许 tranche、杠杆不大于 1、不许做空。
func TestResolveEquityFollowsSpotLimits(t *testing.T) {
	client := newFakeClient("equity")
	client.tagMarkets["binance_equity"] = "equity"
	resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, exampleDSL))
	if err != nil || !resolved.SpotOnly() || resolved.Market.Select != "equity" {
		t.Fatalf("equity 应按现货解析：%+v err=%v", resolved, err)
	}
	levered := parseStrategy(t, "name: levered\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: {total: 0.6}}\n  - {id: b, type: rank, score: volume, select: {top: 1}, weight: {total: 0.6}}\nportfolio:\n  leverage: 1.2\n")
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, levered); err == nil || !strings.Contains(err.Error(), "leverage") {
		t.Fatalf("equity 应拒绝杠杆：%v", err)
	}
	short := parseStrategy(t, "name: short\nrules:\n  - {id: s, type: rank, score: close, select: {bottom: 1}, weight: {total: 0.5}, side: short}\n")
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, short); err == nil || !strings.Contains(err.Error(), "做空") {
		t.Fatalf("equity 应拒绝做空：%v", err)
	}
	tranche := parseStrategy(t, `name: tranche_demo
bar: 1h
universe: {}
rules:
  - id: r
    type: tranche
    weight: {total: 1}
    tranche:
      hold_count: candidate_rows
      score: {method: rank_sum, missing: median}
      sizing: {method: quantity, scale: 0.5}
      hold_period: 3
      long_weight: 2
      short_weight: 0
      long_coin_num: 1
      short_coin_num: 0
      long_factors: [{factor: "close", descending: true, weight: 1.0}]
      short_factors: []
      filter_before: []
      filter_after: []
`)
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, tranche); err == nil || !strings.Contains(err.Error(), "tranche") {
		t.Fatalf("equity 应拒绝 tranche：%v", err)
	}
	// 数据集属性直接声明 equity 也接受。
	source := client.datasets["ds_kline"]
	source.Attributes = map[string]string{"market_type": "equity"}
	source.SubjectTags = nil
	client.datasets["ds_kline"] = source
	if resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, exampleDSL)); err != nil || !resolved.SpotOnly() {
		t.Fatalf("数据集属性 market_type=equity 应按现货解析：%+v err=%v", resolved, err)
	}
}

// A 股 5m 的因子结果 View 能解析：日历继承自源数据集，按交易时段对齐。
func TestResolveCNStockIntradayView(t *testing.T) {
	client := newFakeClient("spot")
	client.views["view_factor_cn_5m"] = ViewInfo{ViewID: "view_factor_cn_5m", DatasetID: "ds_cn_factor", Frequency: "5m", Status: "active", ActiveIndexID: "idx_cn", Columns: []ViewColumn{plainColumn("close")}}
	client.datasets["ds_cn_factor"] = DatasetInfo{DatasetID: "ds_cn_factor", Status: "active", Frequency: "5m", Retention: "7200h", Attributes: map[string]string{"source_dataset_id": "ds_cn_kline", "dataset_role": "factor_result"}}
	client.datasets["ds_cn_kline"] = DatasetInfo{DatasetID: "ds_cn_kline", Status: "active", Frequency: "5m", Retention: "7200h", Alignment: "session", Attributes: map[string]string{"calendar": "cn_stock", "market_type": "equity"}}
	strategy := parseStrategy(t, "name: cn_5m\nuniverse: {min_age_bars: 9600}\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n")
	resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_cn_5m"}, strategy)
	if err != nil {
		t.Fatalf("A 股 5m View 应能解析：%v", err)
	}
	if resolved.Calendar != "cn_stock" || resolved.Bar != "5m" || !resolved.SpotOnly() {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	strategy = parseStrategy(t, "name: cn_5m\nuniverse: {min_age_bars: 9601}\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n")
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_cn_5m"}, strategy); err == nil || !strings.Contains(err.Error(), "9600") {
		t.Fatalf("min_age_bars 超过按交易时段折算的保留根数应拒绝：%v", err)
	}
}
