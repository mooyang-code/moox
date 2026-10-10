package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func stockDay(t *testing.T, year int, month time.Month, day int) time.Time {
	t.Helper()
	return time.Date(year, month, day, 0, 0, 0, 0, shanghai(t)).UTC()
}

// 内嵌 A 股日历止于 2026-12-31：最后一个交易日本身仍能换算（只是没有下一根），回放区间枚举到它为止；
// 索引最新一根超出日历时夹回日历之内，历史窗口照常可用；超出范围的报错是中文。
func TestStockCalendarTail(t *testing.T) {
	last, err := FromStorageStart("cn_stock", "1d", stockDay(t, 2026, 12, 31))
	if err != nil || !last.NextEnd.IsZero() || last.PreviousStart.IsZero() {
		t.Fatalf("最后一个交易日应能换算且没有下一根：%+v err=%v", last, err)
	}
	bars, err := ReplayBars("cn_stock", "1d", stockDay(t, 2026, 12, 28), stockDay(t, 2027, 3, 1), DefaultReplayMaxBars)
	if err != nil || len(bars) != 4 || !bars[3].StorageStart.Equal(stockDay(t, 2026, 12, 31)) {
		t.Fatalf("回放区间应枚举到日历的最后一个交易日：%d 根 err=%v", len(bars), err)
	}
	if _, err := FromBarEnd("cn_stock", "1d", time.Date(2027, 1, 5, 15, 0, 0, 0, shanghai(t))); err == nil || !strings.Contains(err.Error(), "超出 A 股内嵌交易日历的范围") || strings.Contains(err.Error(), "calendar") {
		t.Fatalf("超出日历的报错应是中文：%v", err)
	}
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 5}
	view := ViewInfo{ViewID: "view_stock", IndexedFrom: stockDay(t, 2025, 1, 2), IndexedTo: stockDay(t, 2027, 1, 6), SeriesBars: 5000}
	end, err := ReplayWindow(resolved, nil, view, stockDay(t, 2025, 3, 3), stockDay(t, 2025, 7, 1))
	if err != nil || !end.Equal(stockDay(t, 2025, 7, 1)) {
		t.Fatalf("索引最新一根超出日历时历史窗口应照常可用：%s err=%v", end, err)
	}
	if err := checkAgeCoverage(view, resolved, resolved.MinAgeBars); err != nil {
		t.Fatalf("索引最新一根超出日历时启用校验应照常进行：%v", err)
	}
}

// A 股日线的覆盖边界落在非交易日（节假日误写的行）：最晚一根取不晚于它的交易日，最早一根取不早于它的交易日。
func TestCoverageBoundsSnapToTradingDays(t *testing.T) {
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	// 10-03 到 10-05 整段是假期：规整后最早一根（10-08）晚于最晚一根（09-30），报出行都不在交易日上。
	if bounds, err := CoverageBounds(ViewInfo{IndexedFrom: stockDay(t, 2026, 10, 3), IndexedTo: stockDay(t, 2026, 10, 5)}, resolved); err == nil || !strings.Contains(err.Error(), "都不在 A 股交易日上") {
		t.Fatalf("整段落在假期内的覆盖范围应报出原因：%+v err=%v", bounds, err)
	}
	view := ViewInfo{IndexedFrom: stockDay(t, 2026, 9, 27), IndexedTo: stockDay(t, 2026, 10, 5), SeriesBars: 5000}
	bounds, err := CoverageBounds(view, resolved)
	if err != nil || !bounds.From.Equal(stockDay(t, 2026, 9, 28)) || !bounds.To.Equal(stockDay(t, 2026, 9, 30)) || bounds.ToClosed {
		t.Fatalf("覆盖边界应规整到交易日：%+v err=%v", bounds, err)
	}
	if _, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 9, 28), stockDay(t, 2026, 10, 9)); err != nil {
		t.Fatalf("覆盖边界落在节假日时回放仍应可以提交：%v", err)
	}
}

// 只有因子结果沿用源数据集的行键、可以继承源数据集的日历；按 UTC 零点对齐的重采样数据集不能用 A 股日历。
func TestResolveCalendarInheritance(t *testing.T) {
	strategy := parseStrategy(t, "name: x\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n")
	build := func(datasetAttributes map[string]string) *fakeClient {
		client := newFakeClient("spot")
		client.views["view_1d"] = ViewInfo{ViewID: "view_1d", DatasetID: "ds_1d", Frequency: "1d", Status: "active", ActiveIndexID: "idx", Columns: []ViewColumn{plainColumn("close")}}
		client.datasets["ds_1d"] = DatasetInfo{DatasetID: "ds_1d", Status: "active", Frequency: "1d", Retention: "forever", Attributes: datasetAttributes}
		client.datasets["ds_source"] = DatasetInfo{DatasetID: "ds_source", Status: "active", Frequency: "1m", Retention: "forever", Attributes: map[string]string{"calendar": "cn_stock", "market_type": "spot"}}
		return client
	}
	resample := build(map[string]string{"source_dataset_id": "ds_source", "dataset_role": "kline_resample_result", "alignment": "epoch_utc"})
	resolved, _, err := Resolve(context.Background(), resample, "space", "view_1d", strategy)
	if err != nil || resolved.Calendar != DefaultCalendar {
		t.Fatalf("重采样数据集不应继承源数据集的 A 股日历：%+v err=%v", resolved, err)
	}
	factor := build(map[string]string{"source_dataset_id": "ds_source", "dataset_role": "factor_result"})
	if resolved, _, err := Resolve(context.Background(), factor, "space", "view_1d", strategy); err != nil || resolved.Calendar != "cn_stock" {
		t.Fatalf("因子结果应继承源数据集的日历：%+v err=%v", resolved, err)
	}
	misaligned := build(map[string]string{"source_dataset_id": "ds_source", "calendar": "cn_stock", "alignment": "epoch_utc"})
	if _, _, err := Resolve(context.Background(), misaligned, "space", "view_1d", strategy); err == nil || !strings.Contains(err.Error(), "epoch_utc") {
		t.Fatalf("按 UTC 零点对齐的数据集不能使用 A 股日历：%v", err)
	}
}

// 上一根的版本证据只针对经 bars[-1] 读取的因子列：只经 bars[-1] 读 K 线列时不需要。
func TestPreviousFactorsOnlyForFactorColumns(t *testing.T) {
	client := newFakeClient("spot")
	klineOnly := parseStrategy(t, `name: x
rules:
  - {id: r, type: signal, pool: [BTC-USDT], entry: "bars[-1].close < bars[0].close && ma_20 > 0", exit: "close < 1", weight: 1}
`)
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", klineOnly)
	if err != nil || !program.UsesPreviousBar || len(resolved.PreviousFactors) != 0 || len(resolved.ReadinessBinding().PreviousFactors) != 0 {
		t.Fatalf("只经 bars[-1] 读 K 线列时不应要求上一根的因子证据：%+v err=%v", resolved, err)
	}
	withFactor, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil || len(withFactor.PreviousFactors) != 1 || withFactor.PreviousFactors[0] != "ma" {
		t.Fatalf("经 bars[-1] 读取的因子应记录下来：%+v err=%v", withFactor.PreviousFactors, err)
	}
}

// 年龄探针的日历换算是确定性的：换算失败记 config_error，不按基础设施错误重试。
func TestAgeProbeCalendarErrorIsConfigError(t *testing.T) {
	client := newFakeClient("spot")
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock", MinAgeBars: 9500}
	probe := NewAgeProbe(client, "space", resolved, ViewInfo{ViewID: "view_stock"}, map[string]Subject{"600000.SH": subject("600000.SH", true)}, stockDay(t, 2026, 10, 9), 0)
	_, err := probe(context.Background(), []string{"600000.SH"})
	var skip *SkipError
	if !errors.As(err, &skip) || skip.Reason != SkipConfigError || !strings.Contains(skip.Detail, "之前没有交易日") {
		t.Fatalf("日历换算失败应记 config_error 并给出中文说明：%v", err)
	}
}

// 回放提交时同样拒绝永远无法满足的 min_age_bars；覆盖太短、可用起点会落在最新一根之后时直接说明历史不足。
func TestReplayWindowRejectsUnsatisfiableHistory(t *testing.T) {
	resolved := Resolved{ViewID: "view_factor_1h", Bar: "1h", Calendar: DefaultCalendar, MinAgeBars: 150}
	view := ViewInfo{ViewID: "view_factor_1h", IndexedFrom: barStart.Add(-500 * time.Hour), IndexedTo: barStart, SeriesBars: 100}
	if _, err := ReplayWindow(resolved, nil, view, barStart.Add(-50*time.Hour), barStart); err == nil || !strings.Contains(err.Error(), "永远无法满足") {
		t.Fatalf("N 超过每个序列保留的根数应拒绝：%v", err)
	}
	resolved.MinAgeBars = 30
	view = ViewInfo{ViewID: "view_factor_1h", IndexedFrom: barStart.Add(-10 * time.Hour), IndexedTo: barStart, SeriesBars: 100}
	if _, err := ReplayWindow(resolved, nil, view, barStart.Add(-5*time.Hour), barStart); err == nil || !strings.Contains(err.Error(), "暂不能回放") || strings.Contains(err.Error(), "可用起点") {
		t.Fatalf("可用起点会晚于最新一根时应直接说明历史不足：%v", err)
	}
}

// staleClient 让读取返回 ErrStale，GetView 返回给定代次的 View。
type staleClient struct {
	*fakeClient
	generation string
	reads      int
}

func (c *staleClient) QueryRows(context.Context, string, Query) ([]Row, uint64, error) {
	c.reads++
	return nil, 0, ErrStale
}

func (c *staleClient) GetView(_ context.Context, _, viewID string) (ViewInfo, error) {
	return ViewInfo{ViewID: viewID, ActiveIndexID: "idx_b", Generation: c.generation}, nil
}

func (c *staleClient) ViewGeneration(context.Context, string, string) (string, error) {
	return c.generation, nil
}

// 回放读取期间活动索引换了一代：读取器返回 IndexChangedError 交给调用方复查，不静默改读新索引。
func TestRangeLoaderReportsIndexChange(t *testing.T) {
	client := &staleClient{fakeClient: newFakeClient("spot"), generation: "idx_b@b2"}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: ViewInfo{ViewID: "view_factor_1h", ActiveIndexID: "idx_a", Generation: "idx_a@b1"}}
	_, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	var changed *IndexChangedError
	if !errors.As(err, &changed) || changed.View.Generation != "idx_b@b2" || client.reads != 1 || loader.View.Generation != "idx_a@b1" {
		t.Fatalf("应报告换代且不切换读取器：%v reads=%d view=%+v", err, client.reads, loader.View)
	}
}

// View 的代次由活动索引与激活它的构建 ID 组成；没有构建 ID 时退化为活动索引。
func TestGetViewReadsGeneration(t *testing.T) {
	metadata := &metadataStub{view: &storagepb.View{ViewId: "view", DatasetId: "ds", Freq: "1h", Status: "active", ActiveIndexId: "idx_a", Attributes: map[string]string{"moox.active_build_id": "build-7"}}}
	info, err := (&RPCClient{Metadata: metadata}).GetView(context.Background(), "space", "view")
	if err != nil || info.Generation != "idx_a@build-7" {
		t.Fatalf("代次应带构建 ID：%q err=%v", info.Generation, err)
	}
	metadata.view.Attributes = nil
	if info, err := (&RPCClient{Metadata: metadata}).GetView(context.Background(), "space", "view"); err != nil || info.Generation != "idx_a" {
		t.Fatalf("没有构建 ID 时代次是活动索引：%q err=%v", info.Generation, err)
	}
}
