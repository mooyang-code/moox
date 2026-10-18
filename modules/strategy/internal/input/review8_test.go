package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 写入冲突的退避期间回放被取消：醒来后先确认取消，不再读一次 Storage。
func TestRangeLoaderChecksAbortAfterWait(t *testing.T) {
	backoff := rangeStaleBackoff
	rangeStaleBackoff = time.Millisecond
	t.Cleanup(func() { rangeStaleBackoff = backoff })
	aborted := errors.New("回放已取消")
	checks := 0
	client := &staleClient{fakeClient: newFakeClient("spot"), generation: "idx_a@b1"}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Abort: func(context.Context) error {
		// 第一次确认在等待之前（还没取消），之后的确认都看到已取消。
		checks++
		if checks > 1 {
			return aborted
		}
		return nil
	}}
	if _, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour)); !errors.Is(err, aborted) || client.reads != 1 {
		t.Fatalf("等待期间被取消后不应再读：reads=%d err=%v", client.reads, err)
	}
}

// generationProbeClient 的轻量代次读到了新值，完整读取 View 先遇到传输失败、之后读到的仍是原来那一代。
type generationProbeClient struct {
	*fakeClient
	viewErrs int
	views    int
}

func (c *generationProbeClient) QueryRows(context.Context, string, Query) ([]Row, uint64, error) {
	return []Row{{SubjectID: "BTC-USDT", DataTime: barStart, Values: map[string]float64{"close": 1}}}, 1, nil
}

func (c *generationProbeClient) ViewGeneration(context.Context, string, string) (string, error) {
	return "idx_b@b2", nil
}

func (c *generationProbeClient) GetView(_ context.Context, _, viewID string) (ViewInfo, error) {
	c.views++
	if c.viewErrs > 0 {
		c.viewErrs--
		return ViewInfo{}, transport("读取 View view_factor_1h", errors.New("connection refused"))
	}
	return ViewInfo{ViewID: viewID, ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, nil
}

// 发现换代后读取完整 View 的传输失败按分段读取的退避重试；完整读到的仍是同一代时不算换代（不消耗换代名额）。
func TestGenerationChangeReadRetriesAndConfirms(t *testing.T) {
	backoff := rangeRetryBackoff
	rangeRetryBackoff = time.Millisecond
	t.Cleanup(func() { rangeRetryBackoff = backoff })
	client := &generationProbeClient{fakeClient: newFakeClient("spot"), viewErrs: 2}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}, Subjects: []Subject{subject("BTC-USDT", true)}}
	rows, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	if err != nil || len(rows.Bars[barStart.Unix()]) != 1 || client.views != 3 {
		t.Fatalf("应重试读取 View 并确认没有换代：rows=%v views=%d err=%v", rows.Bars, client.views, err)
	}
}

// A 股日线实例不论是否设置 min_age_bars，启用时都检查最晚一行的行键：按 UTC 零点写行的数据集在启用时就拒绝；
// 最早一行可能来自早已停更的序列，不检查；还没有数据时允许启用。
func TestStockEnableChecksRowKeyWithoutMinAge(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	client := newFakeClient("spot")
	client.views["view_stock"] = ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 1, 5), IndexedTo: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), SeriesBars: 500}
	if err := CheckCoverage(context.Background(), client, "space", resolved, now); err == nil || !strings.Contains(err.Error(), "不是上海时间零点") || !strings.Contains(err.Error(), "不能启用实例") {
		t.Fatalf("UTC 零点写行的 A 股数据集应拒绝启用：%v", err)
	}
	client.views["view_stock"] = ViewInfo{ViewID: "view_stock", IndexedFrom: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), IndexedTo: stockDay(t, 2026, 10, 9), SeriesBars: 500}
	if err := CheckCoverage(context.Background(), client, "space", resolved, now); err != nil {
		t.Fatalf("最早一行的遗留数据不应阻止启用：%v", err)
	}
	client.views["view_stock"] = ViewInfo{ViewID: "view_stock", SeriesBars: 500}
	if err := CheckCoverage(context.Background(), client, "space", resolved, now); err != nil {
		t.Fatalf("还没有数据时应允许启用：%v", err)
	}
}

// A 股超出内嵌日历的报错按上海时间书写，与其余 A 股报错一致。
func TestBeyondCalendarErrorUsesShanghaiTime(t *testing.T) {
	err := beyondCalendarError(time.Date(2026, 12, 31, 1, 0, 0, 0, time.UTC))
	if !strings.Contains(err.Error(), "2026-12-31 09:00:00 上海时间 之后的周期") || strings.Contains(err.Error(), "T01:00:00Z") {
		t.Fatalf("应按上海时间书写：%v", err)
	}
}
