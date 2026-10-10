package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// setClock 在测试期间固定“当前时间”。
func setClock(t *testing.T, now time.Time) {
	t.Helper()
	previous := clock
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = previous })
}

// leaksRaw 报告报错里是否夹带了 Storage 的英文原文。
func leaksRaw(err error) bool {
	return strings.Contains(err.Error(), "revision changed") || strings.Contains(err.Error(), "expected=") || strings.Contains(err.Error(), "index")
}

// 日历推进失败时，报错里的日期是推进前的那一天，而不是零值日期 0000-00-00。
func TestCalendarErrorsNameTheDay(t *testing.T) {
	cases := map[string]func() error{
		"AdvanceBarEnd 越过日历末尾": func() error {
			_, err := AdvanceBarEnd("cn_stock", "1d", time.Date(2026, 12, 30, 15, 0, 0, 0, shanghai(t)), 2)
			return err
		},
		"HistoryStart 越过日历起点": func() error {
			_, err := HistoryStart("cn_stock", "1d", stockDay(t, 1991, 1, 4), 100)
			return err
		},
		"ClosedPeriod 超出日历": func() error {
			_, err := ClosedPeriod("cn_stock", "1d", time.Date(2027, 1, 5, 10, 0, 0, 0, shanghai(t)))
			return err
		},
	}
	for name, run := range cases {
		err := run()
		if err == nil || strings.Contains(err.Error(), "0000-00-00") || !strings.Contains(err.Error(), "A 股日期") {
			t.Fatalf("%s 的报错应带推进前的日期：%v", name, err)
		}
	}
	if _, err := AdvanceBarEnd("cn_stock", "1d", time.Date(2026, 12, 30, 15, 0, 0, 0, shanghai(t)), 2); !strings.Contains(err.Error(), "2026-12-31") {
		t.Fatalf("越过末尾时应报日历的最后一天：%v", err)
	}
	client := newFakeClient("spot")
	probe := NewAgeProbe(client, "space", Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 9500}, ViewInfo{ViewID: "view_stock"}, map[string]Subject{"600000.SH": subject("600000.SH", true)}, stockDay(t, 2026, 10, 9), 0)
	if _, err := probe(context.Background(), []string{"600000.SH"}); err == nil || strings.Contains(err.Error(), "0000-00-00") {
		t.Fatalf("探针的 config_error 明细不应出现零值日期：%v", err)
	}
}

// 日历的第一个交易日没有上一根：它本身仍能换算（PreviousStart 留空），从首日开始的回放与覆盖起点都可用。
func TestStockCalendarHead(t *testing.T) {
	first, err := FromStorageStart("cn_stock", "1d", stockDay(t, 1990, 12, 19))
	if err != nil || !first.PreviousStart.IsZero() || first.NextEnd.IsZero() || first.BarIndex != 0 {
		t.Fatalf("首个交易日应能换算且没有上一根：%+v err=%v", first, err)
	}
	bars, err := ReplayBars("cn_stock", "1d", stockDay(t, 1990, 12, 19), stockDay(t, 1990, 12, 22), DefaultReplayMaxBars)
	if err != nil || len(bars) == 0 || !bars[0].StorageStart.Equal(stockDay(t, 1990, 12, 19)) {
		t.Fatalf("从首日开始的回放应包含首日：%+v err=%v", bars, err)
	}
	program, err := dsl.Compile(parseStrategy(t, `name: x
rules:
  - {id: r, type: signal, pool: [600000.SH], entry: "bars[-1].close < bars[0].close", exit: "close < 1", weight: 1}
`), []string{"close"})
	if err != nil {
		t.Fatal(err)
	}
	for _, seriesBars := range []int{0, 10000} {
		resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 5}
		view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 1990, 12, 19), IndexedTo: stockDay(t, 2026, 10, 9), SeriesBars: seriesBars}
		if _, err := ReplayWindow(resolved, program, view, stockDay(t, 2020, 1, 2), stockDay(t, 2020, 6, 1)); err != nil {
			t.Fatalf("数据从日历首日开始时（每序列 %d 根）回放应可以提交：%v", seriesBars, err)
		}
	}
}

// A 股覆盖太短：报历史不足，而不是先往后推越过日历末尾、误报成日历需要更新。
func TestReplayWindowStockShortCoverage(t *testing.T) {
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 120}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 9, 1), IndexedTo: stockDay(t, 2026, 10, 9), SeriesBars: 5000}
	_, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 9, 10), stockDay(t, 2026, 10, 9))
	if err == nil || !strings.Contains(err.Error(), "需要 120 根数据") || strings.Contains(err.Error(), "更新日历") {
		t.Fatalf("覆盖太短应说明历史不足：%v", err)
	}
}

// 覆盖范围无法使用时说明具体原因：整体超出内嵌日历要提示更新日历，而不是“还没有数据”。
func TestCoverageBeyondCalendarSaysWhy(t *testing.T) {
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 5}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2027, 1, 4), IndexedTo: stockDay(t, 2027, 3, 1), SeriesBars: 5000}
	if _, err := CoverageBounds(view, resolved); err == nil || !strings.Contains(err.Error(), "超出 A 股内嵌交易日历的范围") {
		t.Fatalf("覆盖整体超出日历应说明原因：%v", err)
	}
	if _, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 12, 1), stockDay(t, 2027, 2, 1)); err == nil || strings.Contains(err.Error(), "还没有数据") || !strings.Contains(err.Error(), "超出 A 股内嵌交易日历") {
		t.Fatalf("回放提交应说明超出日历：%v", err)
	}
	if err := checkAgeCoverage(view, resolved, 5); err == nil || strings.Contains(err.Error(), "请等数据写入") || !strings.Contains(err.Error(), "超出 A 股内嵌交易日历") {
		t.Fatalf("启用校验应说明超出日历：%v", err)
	}
	if _, err := CoverageBounds(ViewInfo{}, resolved); !errors.Is(err, ErrCoverageUnknown) {
		t.Fatalf("没有统计时应是覆盖未知：%v", err)
	}
}

// 索引最新一根超出内嵌日历时，日历的最后一个交易日已经写完：经提交路径也能回放到它。
func TestReplayWindowIncludesLastCalendarDay(t *testing.T) {
	setClock(t, time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC))
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 1, 5), IndexedTo: stockDay(t, 2027, 1, 8), SeriesBars: 5000}
	end, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 12, 1), stockDay(t, 2027, 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	bars, err := ReplayBars("cn_stock", "1d", stockDay(t, 2026, 12, 1), end, DefaultReplayMaxBars)
	if err != nil || !bars[len(bars)-1].StorageStart.Equal(stockDay(t, 2026, 12, 31)) {
		t.Fatalf("应回放到日历的最后一个交易日：%v err=%v", bars[len(bars)-1].StorageStart, err)
	}
	// 没有超出日历时，最新一根可能还没写完，回放只到它之前。
	view.IndexedTo = stockDay(t, 2026, 12, 31)
	if end, err = ReplayWindow(resolved, nil, view, stockDay(t, 2026, 12, 1), stockDay(t, 2027, 3, 1)); err != nil || !end.Equal(stockDay(t, 2026, 12, 31)) {
		t.Fatalf("最新一根在日历内时终点应截到它之前：%s err=%v", end, err)
	}
}

// A 股日线的行键必须是交易日的上海零点：按 UTC 零点写行的数据集报错，而不是被静默规整后一直读不到行。
func TestStockRowKeyMustBeShanghaiMidnight(t *testing.T) {
	if _, err := FromStorageStart("cn_stock", "1d", time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "不是上海时间零点") {
		t.Fatalf("UTC 零点的行键应报错：%v", err)
	}
	if _, err := FromStorageStart("cn_stock", "1d", stockDay(t, 2026, 10, 9)); err != nil {
		t.Fatalf("上海零点的行键应能换算：%v", err)
	}
}

// A 股日线按上海日期回放：UTC 零点输入的 [09-01, 10-01) 包含 09-01 到 09-30 的交易日；crypto 原样返回。
func TestReplayRangeUsesShanghaiDates(t *testing.T) {
	start, end, err := ReplayRange("cn_stock", "1d", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !start.Equal(stockDay(t, 2026, 9, 1)) || !end.Equal(stockDay(t, 2026, 10, 1)) {
		t.Fatalf("区间两端应换成上海日期零点：%s ~ %s err=%v", start, end, err)
	}
	bars, err := ReplayBars("cn_stock", "1d", start, end, DefaultReplayMaxBars)
	if err != nil || !bars[0].StorageStart.Equal(stockDay(t, 2026, 9, 1)) || !bars[len(bars)-1].StorageStart.Equal(stockDay(t, 2026, 9, 30)) {
		t.Fatalf("应包含 09-01 至 09-30：%s ~ %s err=%v", bars[0].StorageStart, bars[len(bars)-1].StorageStart, err)
	}
	if _, _, err := ReplayRange("cn_stock", "1d", time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("同一上海日期内的区间应报错")
	}
	cryptoStart, cryptoEnd, err := ReplayRange(DefaultCalendar, "1h", barStart, barStart.Add(time.Hour))
	if err != nil || !cryptoStart.Equal(barStart) || !cryptoEnd.Equal(barStart.Add(time.Hour)) {
		t.Fatalf("crypto 区间应原样返回：%s ~ %s err=%v", cryptoStart, cryptoEnd, err)
	}
}

// min_age_bars 等于每序列保留的根数时回放永远没有可用的 bar（可用起点恰是最新一根，而回放只到它之前）。
func TestReplayWindowRejectsMinAgeEqualToSeriesBars(t *testing.T) {
	resolved := Resolved{ViewID: "view_factor_1h", Bar: "1h", Calendar: DefaultCalendar, MinAgeBars: 100}
	view := ViewInfo{ViewID: "view_factor_1h", IndexedFrom: barStart.Add(-500 * time.Hour), IndexedTo: barStart, SeriesBars: 100}
	if _, err := ReplayWindow(resolved, nil, view, barStart.Add(-50*time.Hour), barStart); err == nil || !strings.Contains(err.Error(), "永远无法满足") {
		t.Fatalf("N 等于每序列根数应拒绝：%v", err)
	}
	resolved.MinAgeBars = 99
	if _, err := ReplayWindow(resolved, nil, view, barStart.Add(-time.Hour), barStart); err != nil {
		t.Fatalf("N 比每序列根数少一根时最新一根之前那根可以回放：%v", err)
	}
}

// scriptedClient 按脚本返回读取结果：stale 次 ErrStale 之后成功；每次读取后可以改 View 的代次。
type scriptedClient struct {
	*fakeClient
	stale      int
	reads      int
	afterRead  func(reads int)
	generation string
}

func (c *scriptedClient) QueryRows(context.Context, string, Query) ([]Row, uint64, error) {
	c.reads++
	defer func() {
		if c.afterRead != nil {
			c.afterRead(c.reads)
		}
	}()
	if c.reads <= c.stale {
		return nil, 0, &staleError{message: "View view_factor_1h 在读取期间有新的写入", raw: errors.New("active View index revision changed: expected=1 actual=2")}
	}
	return []Row{{SubjectID: "BTC-USDT", DataTime: barStart, Values: map[string]float64{"close": 1}}}, 1, nil
}

func (c *scriptedClient) GetView(_ context.Context, _, viewID string) (ViewInfo, error) {
	return ViewInfo{ViewID: viewID, ActiveIndexID: "idx_a", Generation: c.generation}, nil
}

func (c *scriptedClient) ViewGeneration(context.Context, string, string) (string, error) {
	return c.generation, nil
}

func fastStaleRetries(t *testing.T, budget time.Duration) {
	t.Helper()
	backoff, maxBackoff, total := rangeStaleBackoff, rangeStaleMaxBackoff, rangeStaleBudget
	rangeStaleBackoff, rangeStaleMaxBackoff, rangeStaleBudget = time.Millisecond, 4*time.Millisecond, budget
	t.Cleanup(func() { rangeStaleBackoff, rangeStaleMaxBackoff, rangeStaleBudget = backoff, maxBackoff, total })
}

// 活跃 View 的分段读取常与写入冲突（修订号变化）：同一代索引时退避后重读，而不是连续三次冲突就让整个回放失败。
func TestRangeLoaderRetriesRevisionConflicts(t *testing.T) {
	fastStaleRetries(t, time.Minute)
	client := &scriptedClient{fakeClient: newFakeClient("spot"), stale: 6, generation: "idx_a@b1"}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Subjects: []Subject{subject("BTC-USDT", true)}}
	rows, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	if err != nil || client.reads != 7 || len(rows.Bars[barStart.Unix()]) != 1 {
		t.Fatalf("写入冲突应退避重读直到成功：reads=%d err=%v", client.reads, err)
	}
}

// 写入冲突持续到时长预算用完才放弃：报错是中文，说明是持续写入而不是索引重建，Storage 的原文只留给日志。
func TestRangeLoaderGivesUpAfterBudget(t *testing.T) {
	fastStaleRetries(t, 20*time.Millisecond)
	client := &scriptedClient{fakeClient: newFakeClient("spot"), stale: 1 << 30, generation: "idx_a@b1"}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Subjects: []Subject{subject("BTC-USDT", true)}}
	_, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	if err == nil || !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "持续有新的写入") || strings.Contains(err.Error(), "索引持续变化") || leaksRaw(err) {
		t.Fatalf("预算用完应给出中文说明：%v", err)
	}
	if raw := RawCause(err); raw == nil || !strings.Contains(raw.Error(), "revision changed") {
		t.Fatalf("Storage 的原文应留给日志：%v", raw)
	}
}

// 两段读取之间重建两次回到同一个槽位（A→B→A）：按槽位名固定的读取会直接成功，读完也要比较代次。
func TestRangeLoaderDetectsSlotReuse(t *testing.T) {
	client := &scriptedClient{fakeClient: newFakeClient("spot"), generation: "idx_a@b3"}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Subjects: []Subject{subject("BTC-USDT", true)}}
	_, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	var changed *IndexChangedError
	if !errors.As(err, &changed) || changed.View.Generation != "idx_a@b3" {
		t.Fatalf("槽位名相同但代次变了应报告换代：%v", err)
	}
}

// Storage 的业务错误只给中文概述与错误码，原文留给日志；读取期间索引变化的报错也是中文。
func TestStorageErrorsAreChinese(t *testing.T) {
	err := retError("读取数据集 ds", &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "dataset_id is required"})
	var service *ServiceError
	if !errors.As(err, &service) || err.Error() != "读取数据集 ds失败：请求参数无效（INVALID_PARAM）" {
		t.Fatalf("业务错误应是中文概述：%v", err)
	}
	if raw := RawCause(err); raw == nil || raw.Error() != "dataset_id is required" {
		t.Fatalf("原文应留给日志：%v", raw)
	}
	stale := viewRetError("读取 View v 的行", "v", nil, &commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "active View index revision changed: expected=1 actual=2"})
	if !errors.Is(stale, ErrStale) || leaksRaw(stale) {
		t.Fatalf("索引变化应是中文的 ErrStale：%v", stale)
	}
	other := viewRetError("读取 View v 的行", "v", nil, &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "duckdb: io error"})
	if !errors.As(other, &service) || strings.Contains(other.Error(), "duckdb") {
		t.Fatalf("其他 DataView 错误应是中文概述：%v", other)
	}
}

// 启用前的日历截止日检查在输入层由 StockCalendarReadiness 完成：过了截止日返回 ErrCalendarExpired。
func TestStockCalendarReadinessCutoff(t *testing.T) {
	if err := StockCalendarReadiness(time.Date(2026, 12, 30, 10, 0, 0, 0, shanghai(t)), 0, 2); !errors.Is(err, marketcalendar.ErrCalendarExpired) {
		t.Fatalf("过了可用截止日应返回过期：%v", err)
	}
	if err := StockCalendarReadiness(time.Date(2026, 12, 29, 10, 0, 0, 0, shanghai(t)), 0, 2); err != nil {
		t.Fatalf("截止日当天仍可用：%v", err)
	}
}
