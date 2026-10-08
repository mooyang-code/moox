package input

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

func rowsAt(at time.Time, values map[string]map[string]float64) []Row {
	rows := make([]Row, 0, len(values))
	for id, columns := range values {
		rows = append(rows, Row{SubjectID: id, SeriesTag: "venue:binance", DataTime: at, Values: columns})
	}
	return rows
}

func TestLoadBarBuildsFrameWithPreviousBar(t *testing.T) {
	client := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		if query.Start.Equal(barStart) {
			return rowsAt(barStart, map[string]map[string]float64{
				"BTC-USDT": {"close": 101, "ma_20": 100},
				"ETH-USDT": {"close": 10, "ma_20": 9, "bias_q_20": 0.5, "quote_volume_mean_20": 3e6, "quote_volume_mean_q_20": 0.4},
			}), 9, nil
		}
		return rowsAt(query.Start, map[string]map[string]float64{"BTC-USDT": {"close": 99, "ma_20": 100}}), 9, nil
	}
	loader := Loader{Client: client}
	loaded, err := loader.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, EventUniverse: []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}, Readiness: readiness.Result{FailedByFactor: map[string][]string{"qv": {"SOL-USDT"}}}})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	frame := loaded.Frame
	if !frame.BarEnd.Equal(barStart.Add(time.Hour)) || !frame.Spot || loaded.IndexID != "idx_a" || loaded.Revision != 9 {
		t.Fatalf("帧元数据不符：%+v loaded=%+v", frame, loaded)
	}
	if !reflect.DeepEqual(SortedInstruments(frame), []string{"BTC-USDT", "ETH-USDT"}) {
		t.Fatalf("帧中的标的不符：%v", SortedInstruments(frame))
	}
	btc := frame.Rows["BTC-USDT"]
	if btc.Values["close"] != 101 || btc.Previous["close"] != 99 {
		t.Fatalf("BTC 的当期与上一根不符：%+v", btc)
	}
	if frame.Rows["ETH-USDT"].Previous != nil {
		t.Fatalf("没有上一根的标的 Previous 应为空：%+v", frame.Rows["ETH-USDT"])
	}
	if _, failed := frame.FailedColumns["quote_volume_mean_20"]["SOL-USDT"]; !failed {
		t.Fatalf("因子失败应映射到其产出列：%v", frame.FailedColumns)
	}
	if _, failed := frame.FailedColumns["ma_20"]["SOL-USDT"]; failed {
		t.Fatalf("因子失败不应映射到其他因子的列：%v", frame.FailedColumns)
	}
	if len(client.queries) != 2 || client.queries[1].ExpectedRevision != 9 || !client.queries[1].Start.Equal(barStart.Add(-time.Hour)) {
		t.Fatalf("上一根的读取应固定修订号：%+v", client.queries)
	}
	columns := strings.Join(client.queries[0].Columns, ",")
	for _, column := range []string{"close", "ma_20", "bias_q_20", "quote_volume_mean_20", "quote_volume_mean_q_20"} {
		if !strings.Contains(columns, column) {
			t.Fatalf("读取列应包含 %s：%s", column, columns)
		}
	}
	if !reflect.DeepEqual(frame.Expected["btc_trend"], []string{"BTC-USDT"}) || !reflect.DeepEqual(frame.Expected["long_momentum"], []string{"ETH-USDT", "SOL-USDT"}) {
		t.Fatalf("帧的预期集合不符：%v", frame.Expected)
	}
}

func TestLoadBarMapsViewLevelFailuresToAllColumns(t *testing.T) {
	client := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) { return nil, 1, nil }
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, Readiness: readiness.Result{FailedSubjects: []string{"ETH-USDT"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"close", "ma_20", "bias_q_20"} {
		if _, failed := loaded.Frame.FailedColumns[column]["ETH-USDT"]; !failed {
			t.Fatalf("View 级失败应映射到列 %s：%v", column, loaded.Frame.FailedColumns)
		}
	}
}

func TestLoadBarRejectsAmbiguousSeriesAndPropagatesStale(t *testing.T) {
	client := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		return []Row{
			{SubjectID: "ETH-USDT", SeriesTag: "venue:binance", DataTime: query.Start, Values: map[string]float64{"close": 1}},
			{SubjectID: "ETH-USDT", SeriesTag: "venue:okx", DataTime: query.Start, Values: map[string]float64{"close": 2}},
		}, 1, nil
	}
	loader := Loader{Client: client}
	_, err = loader.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	var skip *SkipError
	if !errors.As(err, &skip) || skip.Reason != SkipAmbiguousSeries {
		t.Fatalf("多序列应记为 ambiguous_series：%v", err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) { return nil, 0, ErrStale }
	if _, err := loader.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart}); !errors.Is(err, ErrStale) {
		t.Fatalf("索引变化应透传 ErrStale：%v", err)
	}
	client.errors["subjects"] = errors.New("storage timeout")
	if _, err := loader.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart}); err == nil || errors.As(err, &skip) {
		t.Fatalf("基础设施错误不应变成跳过：%v", err)
	}
}

func TestLoadBarDetectsConfigDrift(t *testing.T) {
	client := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	view := client.views["view_factor_1h"]
	view.Columns = view.Columns[:7]
	client.views["view_factor_1h"] = view
	_, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	var skip *SkipError
	if !errors.As(err, &skip) || skip.Reason != SkipConfigError || !strings.Contains(skip.Detail, "已不存在") {
		t.Fatalf("列被删除应记为 config_error：%v", err)
	}
	view.Columns = newFakeClient("spot").views["view_factor_1h"].Columns
	view.Frequency = "4h"
	client.views["view_factor_1h"] = view
	_, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	if !errors.As(err, &skip) || skip.Reason != SkipConfigError {
		t.Fatalf("周期变化应记为 config_error：%v", err)
	}
}

func TestInstrumentFailuresMapsSubjectIDs(t *testing.T) {
	subjects := map[string]Subject{"BTCUSDT": {SubjectID: "btc", InstrumentID: "BTCUSDT"}}
	mapped := InstrumentFailures(map[string]map[string]struct{}{"close": {"btc": {}, "unknown": {}}}, subjects)
	if _, ok := mapped["close"]["BTCUSDT"]; !ok {
		t.Fatalf("subject_id 应映射为 instrument_id：%v", mapped)
	}
	if _, ok := mapped["close"]["unknown"]; !ok {
		t.Fatalf("未知标的应原样保留：%v", mapped)
	}
}

// 只在 bars[-1] 中引用的列、min_age_bars 探针读取的 close 被删除，都是确定性的 config_error，不能当基础设施错误重试。
func TestLoadBarChecksPreviousAndProbeColumns(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, `name: prev_only
universe:
  min_age_bars: 2
rules:
  - id: r
    type: rank
    score: "bias_q_20 - bars[-1].ma_20"
    select: {top: 1}
    weight: {total: 1}
`)
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", strategy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(program.Columns, ","), "ma_20") {
		t.Fatalf("ma_20 只应出现在上一根列中：%v", program.Columns)
	}
	for _, removed := range []string{"ma_20", "close"} {
		drifted := newFakeClient("spot")
		view := drifted.views["view_factor_1h"]
		kept := view.Columns[:0:0]
		for _, column := range view.Columns {
			if column.Name != removed {
				kept = append(kept, column)
			}
		}
		view.Columns = kept
		drifted.views["view_factor_1h"] = view
		_, err := Loader{Client: drifted}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
		var skip *SkipError
		if !errors.As(err, &skip) || skip.Reason != SkipConfigError || !strings.Contains(skip.Detail, removed) {
			t.Fatalf("删除 %s 应记为 config_error：%v", removed, err)
		}
		if len(drifted.queries) != 0 {
			t.Fatalf("缺列时不应再读行：%+v", drifted.queries)
		}
	}
}

// 年龄探针在当期主查询之后执行，并固定同一修订号；View 覆盖不到目标根时整期跳过（history_insufficient），
// 不能把全部标的当作新上市剔除。
func TestLoadBarPinsProbeRevisionAndChecksCoverage(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, strings.Replace(exampleDSL, "universe:\n", "universe:\n  min_age_bars: 24\n", 1))
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", strategy)
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		return rowsAt(query.Start, map[string]map[string]float64{"ETH-USDT": {"close": 10, "ma_20": 9, "bias_q_20": 0.5, "quote_volume_mean_20": 3e6, "quote_volume_mean_q_20": 0.4}}), 11, nil
	}
	if _, err := (Loader{Client: client}).LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart}); err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if len(client.queries) != 3 || !client.queries[0].Start.Equal(barStart) || client.queries[0].ExpectedRevision != 0 {
		t.Fatalf("第一个查询应是当期主查询：%+v", client.queries)
	}
	for _, query := range client.queries[1:] {
		if query.ExpectedRevision != 11 {
			t.Fatalf("探针与上一根应固定主查询的修订号：%+v", client.queries)
		}
	}

	view := client.views["view_factor_1h"]
	view.IndexedFrom = barStart.Add(-10 * time.Hour)
	client.views["view_factor_1h"] = view
	client.queries = nil
	_, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	var skip *SkipError
	if !errors.As(err, &skip) || skip.Reason != SkipHistoryInsufficient || !strings.Contains(skip.Detail, "min_age_bars=24") {
		t.Fatalf("View 覆盖不足应记为 history_insufficient：%v", err)
	}
}
