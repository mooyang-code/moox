package input

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveExampleStrategy(t *testing.T) {
	client := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resolved.Bar != "1h" || resolved.Calendar != DefaultCalendar || !resolved.Spot || resolved.MarketType != "spot" || resolved.SourceDatasetID != "ds_kline" || resolved.RetentionBars != 720 {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	if resolved.Factors["ma"] != "sha256:ma" || resolved.Factors["bias"] != "sha256:bias" || resolved.Factors["qv"] != "sha256:qv" || len(resolved.Factors) != 3 {
		t.Fatalf("因子指纹不符：%v", resolved.Factors)
	}
	if binding := resolved.Columns["ma_20"]; binding.Source != SourceFactor || binding.FactorID != "ma" || binding.DefinitionHash != "sha256:ma" {
		t.Fatalf("ma_20 的绑定不符：%+v", binding)
	}
	if binding := resolved.Columns["close"]; binding.Source != SourceDataset || binding.FactorID != "" {
		t.Fatalf("close 的绑定不符：%+v", binding)
	}
	if !resolved.UsesPreviousBar || !program.UsesPreviousBar {
		t.Fatal("示例使用了 bars[-1]，应标记 UsesPreviousBar")
	}
	if got := resolved.ColumnsOfFactor("qv"); len(got) != 2 || got[0] != "quote_volume_mean_20" {
		t.Fatalf("因子 qv 的列不符：%v", got)
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseResolved(raw)
	if err != nil || parsed.ViewID != "view_factor_1h" || len(parsed.ViewColumns) != 11 {
		t.Fatalf("resolved_json 往返失败：%+v err=%v", parsed, err)
	}
	if _, err := Compile(parsed, exampleDSL); err != nil {
		t.Fatalf("按固化列重新编译失败：%v", err)
	}
	binding := resolved.ReadinessBinding()
	if !binding.UsesPreviousBar || binding.Factors["ma"] != "sha256:ma" {
		t.Fatalf("就绪绑定不符：%+v", binding)
	}
}

// 验收项 4：引用不存在的列时返回明确的列名错误并列出可用列。
func TestResolveReportsMissingColumns(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, `name: missing
rules:
  - id: r
    type: rank
    score: "rank(momentum_99)"
    select: {top: 1}
    weight: {total: 1}
`)
	_, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", strategy)
	if err == nil || !strings.Contains(err.Error(), "momentum_99") || !strings.Contains(err.Error(), "可用列") || !strings.Contains(err.Error(), "ma_20") {
		t.Fatalf("应报告缺失列并列出可用列：%v", err)
	}
}

func TestResolveRejectsBarAssertionMismatch(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, strings.Replace(exampleDSL, "bar: 1h", "bar: 4h", 1))
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", strategy); err == nil || !strings.Contains(err.Error(), "bar 断言") {
		t.Fatalf("bar 断言不符应拒绝：%v", err)
	}
}

// S21（启用部分）：现货 View 拒绝 leverage > 1 与做空；合约 View 允许。
func TestResolveSpotRejectsLeverageAndShort(t *testing.T) {
	levered := parseStrategy(t, `name: levered
rules:
  - id: a
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.6}
  - id: b
    type: rank
    score: "volume"
    select: {top: 1}
    weight: {total: 0.6}
portfolio:
  leverage: 1.2
`)
	if _, _, err := Resolve(context.Background(), newFakeClient("spot"), "space", "view_factor_1h", levered); err == nil || !strings.Contains(err.Error(), "leverage") {
		t.Fatalf("现货应拒绝杠杆：%v", err)
	}
	if resolved, _, err := Resolve(context.Background(), newFakeClient("swap"), "space", "view_factor_1h", levered); err != nil || resolved.Spot {
		t.Fatalf("合约应允许杠杆：%+v err=%v", resolved, err)
	}
	short := parseStrategy(t, `name: short
rules:
  - id: s
    type: rank
    score: "close"
    select: {bottom: 1}
    weight: {total: 0.5}
    side: short
`)
	if _, _, err := Resolve(context.Background(), newFakeClient("spot"), "space", "view_factor_1h", short); err == nil || !strings.Contains(err.Error(), "做空") {
		t.Fatalf("现货应拒绝做空：%v", err)
	}
	if _, _, err := Resolve(context.Background(), newFakeClient("swap"), "space", "view_factor_1h", short); err != nil {
		t.Fatalf("合约应允许做空：%v", err)
	}
}

func TestResolveRejectsMinAgeBeyondRetentionAndMissingHash(t *testing.T) {
	client := newFakeClient("spot")
	tooOld := parseStrategy(t, strings.Replace(exampleDSL, "universe:\n", "universe:\n  min_age_bars: 800\n", 1))
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", tooOld); err == nil || !strings.Contains(err.Error(), "保留根数") {
		t.Fatalf("min_age_bars 超过保留根数应拒绝：%v", err)
	}
	client.factors["ma"] = FactorInfo{FactorID: "ma"}
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err == nil || !strings.Contains(err.Error(), "definition_hash") {
		t.Fatalf("缺少指纹应拒绝：%v", err)
	}
	client = newFakeClient("spot")
	view := client.views["view_factor_1h"]
	view.Status = "building"
	client.views["view_factor_1h"] = view
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("非 active 的 View 应拒绝：%v", err)
	}
}

// 市场类型：数据集属性优先，否则由源数据集标签判定；标签不一致或未知时拒绝。
func TestResolveMarketTypeFromAttributesOrTags(t *testing.T) {
	client := newFakeClient("swap")
	resolved, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil || resolved.Spot || resolved.MarketType != "swap" {
		t.Fatalf("binance_swap 标签应判定为合约：%+v err=%v", resolved, err)
	}
	source := client.datasets["ds_kline"]
	source.Attributes = map[string]string{"market_type": "spot"}
	client.datasets["ds_kline"] = source
	if resolved, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err != nil || !resolved.Spot {
		t.Fatalf("数据集属性应优先：%+v err=%v", resolved, err)
	}
	source.Attributes = map[string]string{}
	source.SubjectTags = []string{"binance_spot", "binance_swap"}
	client.datasets["ds_kline"] = source
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err == nil || !strings.Contains(err.Error(), "无法确定市场类型") {
		t.Fatalf("标签不一致应拒绝：%v", err)
	}
	source.SubjectTags = nil
	client.datasets["ds_kline"] = source
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err == nil || !strings.Contains(err.Error(), "无法确定市场类型") {
		t.Fatalf("没有标签与属性应拒绝：%v", err)
	}
	source.SubjectTags = []string{"mixed_bad"}
	client.datasets["ds_kline"] = source
	if _, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err == nil || !strings.Contains(err.Error(), "不受支持") {
		t.Fatalf("未知市场类型应拒绝：%v", err)
	}
}

func TestRetentionBars(t *testing.T) {
	for _, tc := range []struct {
		retention, bar string
		bars           int
		ok             bool
	}{
		{"720h", "1h", 720, true}, {"720h", "4h", 180, true}, {"48h", "1d", 2, true}, {"forever", "1h", 0, false}, {"", "1h", 0, false}, {"abc", "1h", 0, false},
	} {
		bars, ok := retentionBars(tc.retention, tc.bar)
		if bars != tc.bars || ok != tc.ok {
			t.Fatalf("retentionBars(%s, %s) = %d, %v", tc.retention, tc.bar, bars, ok)
		}
	}
}
