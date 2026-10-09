package input

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// fakeClient 是内存中的 Storage/Factor 替身。
type fakeClient struct {
	views      map[string]ViewInfo
	datasets   map[string]DatasetInfo
	subjects   map[string][]Subject
	tags       map[string][]string
	tagMarkets map[string]string
	factors    map[string]FactorInfo
	rows       func(query Query) ([]Row, uint64, error)
	queries    []Query
	errors     map[string]error
}

func (f *fakeClient) GetView(_ context.Context, _, viewID string) (ViewInfo, error) {
	if err := f.errors["view"]; err != nil {
		return ViewInfo{}, err
	}
	view, ok := f.views[viewID]
	if !ok {
		return ViewInfo{}, fmt.Errorf("View %s 不存在", viewID)
	}
	return view, nil
}

func (f *fakeClient) GetDataset(_ context.Context, _, datasetID string) (DatasetInfo, error) {
	dataset, ok := f.datasets[datasetID]
	if !ok {
		return DatasetInfo{}, fmt.Errorf("数据集 %s 不存在", datasetID)
	}
	return dataset, nil
}

func (f *fakeClient) ListDatasetSubjects(_ context.Context, _, datasetID string) ([]Subject, error) {
	if err := f.errors["subjects"]; err != nil {
		return nil, err
	}
	return append([]Subject(nil), f.subjects[datasetID]...), nil
}

func (f *fakeClient) ListTagMembers(_ context.Context, _, tagID string) ([]string, error) {
	if err := f.errors["tags"]; err != nil {
		return nil, err
	}
	members, ok := f.tags[tagID]
	if !ok {
		return nil, fmt.Errorf("标签 %s 不存在", tagID)
	}
	return append([]string(nil), members...), nil
}

func (f *fakeClient) GetTag(_ context.Context, _, tagID string) (TagInfo, error) {
	if marketType, ok := f.tagMarkets[tagID]; ok {
		return TagInfo{TagID: tagID, MarketType: marketType}, nil
	}
	if _, ok := f.tags[tagID]; ok {
		return TagInfo{TagID: tagID}, nil
	}
	return TagInfo{}, fmt.Errorf("%w：%s", ErrTagNotFound, tagID)
}

func (f *fakeClient) QueryRows(_ context.Context, _ string, query Query) ([]Row, uint64, error) {
	f.queries = append(f.queries, query)
	if f.rows == nil {
		return nil, 0, errors.New("没有行数据")
	}
	return f.rows(query)
}

func (f *fakeClient) GetFactor(_ context.Context, factorID string) (FactorInfo, error) {
	factor, ok := f.factors[factorID]
	if !ok {
		return FactorInfo{}, fmt.Errorf("因子 %s 不存在", factorID)
	}
	return factor, nil
}

func factorColumn(name, factorID string) ViewColumn {
	return ViewColumn{Name: name, Attributes: map[string]string{"origin_factor_id": factorID, "factor_output": name}}
}

func plainColumn(name string) ViewColumn {
	return ViewColumn{Name: name, Attributes: map[string]string{}}
}

func subject(id string, active bool) Subject {
	return Subject{SubjectID: id, SeriesTag: "venue:binance", Active: active, Attributes: map[string]string{}}
}

// newFakeClient 构造一个现货因子 View：K 线列 + ma_20、bias_q_20、quote_volume_mean_20、quote_volume_mean_q_20 因子列。
func newFakeClient(marketType string) *fakeClient {
	return &fakeClient{
		views: map[string]ViewInfo{
			"view_factor_1h": {ViewID: "view_factor_1h", DatasetID: "ds_factor", Frequency: "1h", Status: "active", ActiveIndexID: "idx_a", IndexedFrom: barStart.Add(-500 * time.Hour), IndexedTo: barStart, Columns: []ViewColumn{
				plainColumn("open"), plainColumn("high"), plainColumn("low"), plainColumn("close"), plainColumn("volume"), plainColumn("quote_volume"), plainColumn("trade_num"),
				factorColumn("ma_20", "ma"), factorColumn("bias_q_20", "bias"), factorColumn("quote_volume_mean_20", "qv"), factorColumn("quote_volume_mean_q_20", "qv"),
			}},
		},
		datasets: map[string]DatasetInfo{
			"ds_factor": {DatasetID: "ds_factor", Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{"source_dataset_id": "ds_kline"}},
			"ds_kline":  {DatasetID: "ds_kline", Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{}, SubjectTags: []string{"binance_" + marketType}},
		},
		subjects:   map[string][]Subject{"ds_factor": {subject("BTC-USDT", true), subject("ETH-USDT", true), subject("SOL-USDT", true), subject("USDC-USDT", true), subject("OLD-USDT", false)}},
		tags:       map[string][]string{"stablecoins": {"USDC-USDT"}, "majors": {"BTC-USDT", "ETH-USDT"}},
		tagMarkets: map[string]string{"binance_spot": "spot", "binance_swap": "swap", "mixed_bad": "option"},
		errors:     map[string]error{},
		factors: map[string]FactorInfo{
			"ma":   {FactorID: "ma", DefinitionHash: "sha256:ma", Outputs: []string{"ma_20"}},
			"bias": {FactorID: "bias", DefinitionHash: "sha256:bias", Outputs: []string{"bias_q_20"}},
			"qv":   {FactorID: "qv", DefinitionHash: "sha256:qv", Outputs: []string{"quote_volume_mean_20", "quote_volume_mean_q_20"}},
		},
	}
}

const exampleDSL = `name: binance_spot_momentum_1h
bar: 1h

universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]

rules:
  - id: long_momentum
    type: rank
    filter: "quote_volume_mean_20 > 2000000 && close > 0"
    score: "0.6 * rank(bias_q_20) + 0.4 * rank(quote_volume_mean_q_20)"
    select: {top: 5, buffer: 2}
    weight: {total: 0.8, method: equal, cap: 0.3}

  - id: btc_trend
    type: signal
    pool: [BTC-USDT]
    entry: "bars[-1].close <= bars[-1].ma_20 && bars[0].close > bars[0].ma_20"
    exit:  "bars[0].close < bars[0].ma_20"
    weight: {total: 0.2}

portfolio:
  leverage: 1
  max_weight: 0.3
  min_universe: 1
  max_missing: 0.5
`

func parseStrategy(t *testing.T, raw string) dsl.Strategy {
	t.Helper()
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析 DSL 失败：%v", err)
	}
	return strategy
}

var barStart = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
