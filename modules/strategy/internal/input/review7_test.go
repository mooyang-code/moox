package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A 股日线索引统计的行键不是上海零点（按 UTC 零点写行）：提交回放时就报出，而不是规整后通过、执行时每根都缺数据。
func TestCoverageBoundsRejectsUTCMidnightStockKeys(t *testing.T) {
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IndexedTo: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), SeriesBars: 5000}
	if _, err := CoverageBounds(view, resolved, testNow); err == nil || !strings.Contains(err.Error(), "不是上海时间零点") {
		t.Fatalf("UTC 零点的行键应报错：%v", err)
	}
	if _, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 9, 10), stockDay(t, 2026, 10, 1), testNow); err == nil || !strings.Contains(err.Error(), "不是上海时间零点") {
		t.Fatalf("提交回放时就应报出：%v", err)
	}
}

// 日历末日只有在当前时间已过日历、并且索引里确有之后的数据时才算写完；当前时间还在日历之内时，日历之外的行是误写的
// 未来数据：最晚一根夹到已闭合的最后一根，不算超出日历。
func TestToClosedRequiresRealDaysBeyondCalendar(t *testing.T) {
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 1, 5), IndexedTo: stockDay(t, 2027, 1, 8), SeriesBars: 5000}
	inside := time.Date(2026, 12, 31, 10, 0, 0, 0, shanghai(t))
	bounds, err := CoverageBounds(view, resolved, inside)
	if err != nil || bounds.ToClosed || bounds.Beyond != 0 || !bounds.To.Equal(stockDay(t, 2026, 12, 30)) {
		t.Fatalf("日历之内时未来行不算超出日历，最晚一根夹到已闭合的 12-30：%+v err=%v", bounds, err)
	}
	end, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 12, 1), stockDay(t, 2027, 3, 1), inside)
	if err != nil || !end.Equal(stockDay(t, 2026, 12, 30)) {
		t.Fatalf("终点应截到已闭合的最后一根之前：%s err=%v", end, err)
	}
	after := time.Date(2027, 1, 5, 10, 0, 0, 0, shanghai(t))
	if bounds, err := CoverageBounds(view, resolved, after); err != nil || !bounds.ToClosed || bounds.Beyond != 5 {
		t.Fatalf("已过日历时日历之后的行是真的数据（扣减不超过今天）：%+v err=%v", bounds, err)
	}
}

// 当前时间还在日历之内，索引里一行误写的远期数据：不报“数据超出日历”，回放也不延伸到还没发生的 bar。
func TestFutureRowsAreClampedToNow(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, shanghai(t))
	stock := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 1, 5), IndexedTo: stockDay(t, 2027, 6, 1), SeriesBars: 100}
	end, err := ReplayWindow(stock, nil, view, stockDay(t, 2026, 9, 1), stockDay(t, 2027, 1, 1), now)
	if err != nil || !end.Equal(stockDay(t, 2026, 10, 8)) {
		t.Fatalf("终点应截到 10-08（已闭合的最后一根）之前：%s err=%v", end, err)
	}
	crypto := Resolved{ViewID: "view_factor_1h", Bar: "1h", Calendar: DefaultCalendar}
	future := ViewInfo{ViewID: "view_factor_1h", IndexedFrom: barStart.Add(-500 * time.Hour), IndexedTo: barStart.Add(48 * time.Hour), SeriesBars: 5000}
	bounds, err := CoverageBounds(future, crypto, barStart.Add(30*time.Minute))
	if err != nil || !bounds.To.Equal(barStart.Add(-time.Hour)) {
		t.Fatalf("crypto 的最晚一根也夹到已闭合的最后一根：%+v err=%v", bounds, err)
	}
}

// 索引最新一根超出内嵌日历时，真正的最新一根在日历之外：每个序列保留的根数先扣掉日历之外至多的根数，不把覆盖起点
// 估得偏早；扣完一根都不剩时报历史不足。
func TestCoverageStartDiscountsRowsBeyondCalendar(t *testing.T) {
	now := time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 1, 5), IndexedTo: stockDay(t, 2027, 1, 4), SeriesBars: 10}
	// 日历之外最多 4 个交易日（01-01 至 01-04），日历之内只能确定保留 6 根：覆盖起点是末日往前 5 个交易日。
	want, err := HistoryStart("cn_stock", "1d", stockDay(t, 2026, 12, 31), 6)
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := CoverageBounds(view, resolved, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := coverageStart(bounds, view, resolved, time.Time{}); !got.Equal(want) {
		t.Fatalf("覆盖起点应扣掉日历之外的根数：%s，期望 %s", got, want)
	}
	view.IndexedTo = stockDay(t, 2027, 1, 15)
	if _, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 12, 18), stockDay(t, 2027, 1, 1), now); err == nil || !strings.Contains(err.Error(), "暂不能回放") {
		t.Fatalf("日历之内一根都不能确定保留时应报历史不足：%v", err)
	}
}

// A 股的回放报错按上海日期写时间，不写成前一日 16:00 UTC。
func TestStockReplayErrorsUseShanghaiDates(t *testing.T) {
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 5}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 9, 1), IndexedTo: stockDay(t, 2026, 10, 9), SeriesBars: 5000}
	_, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 9, 1), stockDay(t, 2026, 10, 9), testNow)
	var early *EarlyStartError
	if !errors.As(err, &early) || !strings.Contains(err.Error(), "2026-09-07（上海日期）") || strings.Contains(err.Error(), "T16:00:00Z") {
		t.Fatalf("可用起点应写上海日期：%v", err)
	}
	if label := BarEndLabel("cn_stock", "1d", time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)); label != "2026-10-09T07:00:00Z（上海 2026-10-09 收盘）" {
		t.Fatalf("A 股 bar_end 应注明上海收盘：%s", label)
	}
}

// generationClient 让读取在写入冲突后由 Abort 放弃，或代次复核遇到传输失败。
type generationClient struct {
	*fakeClient
	stale          bool
	generationErrs int
	generations    int
	views          int
}

func (c *generationClient) QueryRows(context.Context, string, Query) ([]Row, uint64, error) {
	if c.stale {
		return nil, 0, &staleError{message: "View view_factor_1h 在读取期间有新的写入"}
	}
	return []Row{{SubjectID: "BTC-USDT", DataTime: barStart, Values: map[string]float64{"close": 1}}}, 1, nil
}

func (c *generationClient) ViewGeneration(context.Context, string, string) (string, error) {
	c.generations++
	if c.generationErrs > 0 {
		c.generationErrs--
		return "", transport("读取 View view_factor_1h", errors.New("connection refused"))
	}
	return "idx_a@b1", nil
}

func (c *generationClient) GetView(_ context.Context, _, viewID string) (ViewInfo, error) {
	c.views++
	return ViewInfo{ViewID: viewID, ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, nil
}

// 写入冲突退避前先问 Abort：回放被取消时立即放弃，不再退避重读；ctx 结束时返回 ctx 的错误。
func TestRangeLoaderAbortsDuringBackoff(t *testing.T) {
	aborted := errors.New("回放已取消")
	client := &generationClient{fakeClient: newFakeClient("spot"), stale: true}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Subjects: []Subject{subject("BTC-USDT", true)}, Abort: func(context.Context) error { return aborted }}
	started := time.Now()
	if _, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour)); !errors.Is(err, aborted) || time.Since(started) > time.Second {
		t.Fatalf("取消后应立即放弃：%v（%s）", err, time.Since(started))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loader.Abort = nil
	if _, err := loader.Load(ctx, barStart, barStart.Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 结束时应返回 ctx 的错误：%v", err)
	}
}

// 读完行后的代次复核只读代次、不读列；传输失败单独计重试，不会因为读行用过重试就丢掉已读的行。
func TestGenerationCheckIsLightAndRetriedSeparately(t *testing.T) {
	backoff := rangeRetryBackoff
	rangeRetryBackoff = time.Millisecond
	t.Cleanup(func() { rangeRetryBackoff = backoff })
	client := &generationClient{fakeClient: newFakeClient("spot"), generationErrs: 2}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Subjects: []Subject{subject("BTC-USDT", true)}}
	rows, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	if err != nil || len(rows.Bars[barStart.Unix()]) != 1 || client.generations != 3 || client.views != 0 {
		t.Fatalf("复核应重试后通过、不读完整 View：rows=%v generations=%d views=%d err=%v", rows.Bars, client.generations, client.views, err)
	}
}

// 写入冲突预算用完的报错把时长写成中文。
func TestDurationText(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Minute: "1 分钟", 90 * time.Second: "90 秒", 20 * time.Millisecond: "20 毫秒"} {
		if got := durationText(d); got != want {
			t.Fatalf("%s 应写成 %q：%q", d, want, got)
		}
	}
}

// View 被删除时报 ErrViewNotFound，说明不带“请重新绑定实例”（回放与启用也用它）。
func TestViewNotFoundIsNeutral(t *testing.T) {
	client := &RPCClient{Metadata: &metadataStub{}}
	_, err := client.GetView(context.Background(), "space", "view_gone")
	if !errors.Is(err, ErrViewNotFound) || err.Error() != "View view_gone 已不存在" {
		t.Fatalf("View 被删除应报 ErrViewNotFound：%v", err)
	}
	if _, err := client.ViewGeneration(context.Background(), "space", "view_gone"); !errors.Is(err, ErrViewNotFound) {
		t.Fatalf("只读代次时同样报 ErrViewNotFound：%v", err)
	}
}
