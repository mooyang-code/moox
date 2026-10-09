package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A 股日线 View（数据集声明 calendar: cn_stock）能解析：日历自检不依赖某个具体日期恰好是交易日。
// bar_end 落在非交易日时给出明确的中文错误；A 股只支持日线。
func TestResolveCNStockDailyView(t *testing.T) {
	client := newFakeClient("spot")
	client.views["view_stock_1d"] = ViewInfo{ViewID: "view_stock_1d", DatasetID: "ds_stock", Frequency: "1d", Status: "active", ActiveIndexID: "idx_s", Columns: []ViewColumn{plainColumn("close")}}
	client.datasets["ds_stock"] = DatasetInfo{DatasetID: "ds_stock", Status: "active", Frequency: "1d", Retention: "forever", Attributes: map[string]string{"calendar": "cn_stock", "market_type": "spot"}}
	client.subjects["ds_stock"] = []Subject{subject("600000.SH", true), subject("600519.SH", true)}
	strategy := parseStrategy(t, `name: stock_daily
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 1}
`)
	resolved, _, err := Resolve(context.Background(), client, "space", "view_stock_1d", strategy)
	if err != nil {
		t.Fatalf("A 股日线 View 应能解析：%v", err)
	}
	if resolved.Calendar != "cn_stock" || resolved.Bar != "1d" {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	location := shanghai(t)
	// 2026-10-08 是国庆假期后的第一个交易日，10-05 是假期。
	period, err := FromBarEnd("cn_stock", "1d", time.Date(2026, 10, 8, 15, 0, 0, 0, location))
	if err != nil || period.StorageStart.In(location).Format("2006-01-02") != "2026-10-08" || period.PreviousStart.In(location).Format("2006-01-02") != "2026-09-30" {
		t.Fatalf("交易日的周期边界不符：%+v err=%v", period, err)
	}
	if _, err := FromBarEnd("cn_stock", "1d", time.Date(2026, 10, 5, 15, 0, 0, 0, location)); err == nil || !strings.Contains(err.Error(), "不在 A 股交易日上") {
		t.Fatalf("非交易日的 bar_end 应给出中文错误：%v", err)
	}
	if err := CheckCalendar("cn_stock", "1h"); !errors.Is(err, ErrUnsupportedCalendar) {
		t.Fatalf("A 股只支持日线：%v", err)
	}
	if err := CheckCalendar("crypto_24x7", "15m"); err != nil {
		t.Fatalf("crypto 支持定长周期：%v", err)
	}
	if err := StockCalendarReadiness(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatalf("内嵌日历在有效期内应可用：%v", err)
	}
}

// 覆盖起点以本期 T 与索引最新一根中较早者为基准：处理第 T 根时索引已经写入更新的 bar，
// 不影响对第 T 根能否追溯 N 根的判断；N 恰好等于每个序列保留的根数时也能满足。
func TestCoverageStartAnchorsAtProcessedBar(t *testing.T) {
	resolved := Resolved{ViewID: "view_factor_1h", Bar: "1h", Calendar: DefaultCalendar, MinAgeBars: 24}
	view := ViewInfo{ViewID: "view_factor_1h", DatasetID: "ds_factor", Frequency: "1h", ActiveIndexID: "idx_a", IndexedFrom: barStart.Add(-1000 * time.Hour), IndexedTo: barStart.Add(2 * time.Hour), SeriesBars: 24}
	if start := CoverageStartAt(view, resolved, barStart); !start.Equal(barStart.Add(-23 * time.Hour)) {
		t.Fatalf("以 T 为基准的覆盖起点不符：%s", start)
	}
	if start := CoverageStart(view, resolved); !start.Equal(barStart.Add(-21 * time.Hour)) {
		t.Fatalf("以索引最新一根为基准的覆盖起点不符：%s", start)
	}
	if start := CoverageStartAt(view, resolved, barStart.Add(10*time.Hour)); !start.Equal(barStart.Add(-21 * time.Hour)) {
		t.Fatalf("基准不能晚于索引最新一根：%s", start)
	}
	if start := CoverageStartAt(ViewInfo{}, resolved, barStart); !start.IsZero() {
		t.Fatalf("覆盖未知时应返回零值：%s", start)
	}

	client := newFakeClient("spot")
	client.rows = func(query Query) ([]Row, uint64, error) {
		return []Row{{SubjectID: "BTC-USDT", DataTime: query.Start, Values: map[string]float64{"close": 1}}}, 7, nil
	}
	subjects := map[string]Subject{"BTC-USDT": subject("BTC-USDT", true)}
	satisfied, err := NewAgeProbe(client, "space", resolved, view, subjects, barStart, 0)(context.Background(), []string{"BTC-USDT"})
	if err != nil {
		t.Fatalf("N 等于保留根数、索引已前进时不应误判历史不足：%v", err)
	}
	if _, ok := satisfied["BTC-USDT"]; !ok {
		t.Fatalf("有行的标的应满足年龄：%v", satisfied)
	}
}

// 覆盖统计只在需要时读取：没有 min_age_bars 的实例每期不读；有 min_age_bars 的只读缓存的统计（非精确）。
func TestLoadBarReadsCoverageOnlyForAgeProbe(t *testing.T) {
	client := newFakeClient("spot")
	client.rows = func(query Query) ([]Row, uint64, error) {
		return rowsAt(query.Start, map[string]map[string]float64{"ETH-USDT": {"close": 10, "ma_20": 9, "bias_q_20": 0.5, "quote_volume_mean_20": 3e6, "quote_volume_mean_q_20": 0.4}}), 11, nil
	}
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	if len(client.coverageReads) != 0 {
		t.Fatalf("Resolve 不应读取覆盖统计：%v", client.coverageReads)
	}
	if _, err := (Loader{Client: client}).LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart}); err != nil {
		t.Fatal(err)
	}
	if len(client.coverageReads) != 0 {
		t.Fatalf("没有 min_age_bars 时不应读取覆盖统计：%v", client.coverageReads)
	}
	aged, agedProgram, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, strings.Replace(exampleDSL, "universe:\n", "universe:\n  min_age_bars: 24\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Loader{Client: client}).LoadBar(context.Background(), "space", aged, agedProgram, Bar{BarStart: barStart}); err != nil {
		t.Fatal(err)
	}
	if len(client.coverageReads) != 1 || client.coverageReads[0] {
		t.Fatalf("年龄探针只应读取一次缓存的统计：%v", client.coverageReads)
	}
	if err := CheckAgeCoverage(context.Background(), client, "space", aged); err != nil {
		t.Fatal(err)
	}
	if len(client.coverageReads) != 2 || !client.coverageReads[1] {
		t.Fatalf("启用时应读取精确的统计：%v", client.coverageReads)
	}
}

// 运行期 DSL 引用的标签被删除：记 config_error 让实例降级，不让 exclude_tags 悄悄失效、按标签选的池变空；
// 读取标签的传输失败按基础设施错误重试，不当作标签不存在。
func TestLoadBarSkipsWhenReferencedTagDeleted(t *testing.T) {
	client := newFakeClient("spot")
	client.rows = func(query Query) ([]Row, uint64, error) {
		return rowsAt(query.Start, map[string]map[string]float64{"ETH-USDT": {"close": 10, "ma_20": 9, "bias_q_20": 0.5, "quote_volume_mean_20": 3e6, "quote_volume_mean_q_20": 0.4}}), 11, nil
	}
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	delete(client.tags, "stablecoins")
	_, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	var skip *SkipError
	if !errors.As(err, &skip) || skip.Reason != SkipConfigError || !strings.Contains(skip.Detail, "标签 stablecoins 已不存在") {
		t.Fatalf("引用的标签被删除应记 config_error：%v", err)
	}
	client.tags["stablecoins"] = []string{"USDC-USDT"}
	client.errors["tag"] = &TransportError{Operation: "读取标签 stablecoins", Err: errors.New("i/o timeout")}
	_, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	if err == nil || errors.As(err, &skip) {
		t.Fatalf("读取标签的传输失败应作为基础设施错误返回：%v", err)
	}
}
