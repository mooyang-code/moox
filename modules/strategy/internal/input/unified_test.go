package input

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	spotTag = "venue:binance|market:spot|source:spot_http"
	swapTag = "venue:binance|market:swap|source:swap_http"
)

// unifiedDSL 是统一数据集上的策略：必须写 market。
const unifiedDSL = `name: unified
market: {select: both, prefer: swap}
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 1}
    weight: 1
portfolio: {max_missing: 1}
`

// newUnifiedClient 构造统一数据集上的因子任务（ftask_1h）：一个结果 View，源数据集 market_type=multi，
// 挂现货、永续两个标签；现货标签有 BTC、ETH、SOL，永续标签有 BTC、ETH、DOGE。
func newUnifiedClient() *fakeClient {
	client := newFakeClient("spot")
	client.datasets["ds_kline"] = DatasetInfo{DatasetID: "ds_kline", Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{"market_type": "multi"}, SubjectTags: []string{"binance_spot", "binance_swap"}}
	factor := client.datasets["ds_factor"]
	factor.SubjectTags = []string{"binance_spot", "binance_swap"}
	client.datasets["ds_factor"] = factor
	client.subjects["ds_factor"] = []Subject{
		{SubjectID: "BTC-USDT", SeriesTag: spotTag, Active: true}, {SubjectID: "ETH-USDT", SeriesTag: spotTag, Active: true},
		{SubjectID: "SOL-USDT", SeriesTag: spotTag, Active: true}, {SubjectID: "DOGE-USDT", SeriesTag: swapTag, Active: true},
		{SubjectID: "OLD-USDT", SeriesTag: spotTag, Active: false},
	}
	client.tags["binance_spot"] = []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}
	client.tags["binance_swap"] = []string{"BTC-USDT", "ETH-USDT", "DOGE-USDT"}
	return client
}

func TestResolveUnifiedDatasetRequiresMarket(t *testing.T) {
	client := newUnifiedClient()
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, unifiedDSL))
	if err != nil {
		t.Fatalf("统一数据集上的任务应能解析：%v", err)
	}
	if !resolved.Unified() || len(resolved.Tasks) != 1 || resolved.Tasks[MarketMulti].ViewID != "view_factor_1h" || program == nil {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	if got := strings.Join(resolved.Markets(), ","); got != "spot,swap" {
		t.Fatalf("市场按 DSL 的模式取：%s", got)
	}
	if resolved.TaskOf("swap").ViewID != "view_factor_1h" || resolved.TaskKey("spot") != MarketMulti || strings.Join(resolved.ViewIDs(), ",") != "view_factor_1h" {
		t.Fatalf("两个市场共用一个 View：%+v", resolved)
	}
	if err := CheckResolvedMarkets(resolved, map[string]string{MarketMulti: "ftask_1h"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resolved)
	parsed, err := ParseResolved(raw)
	if err != nil || !parsed.Unified() {
		t.Fatalf("resolved_json 往返失败：%+v err=%v", parsed, err)
	}

	// 五种模式在统一数据集上都能解析；纯现货、纯永续只涉及一个市场。
	for _, mode := range []string{"{select: spot, prefer: spot}", "{select: swap, prefer: swap}", "{select: both, prefer: spot}", "{select: both, prefer: swap}", "{select: spot, prefer: swap}"} {
		dslText := strings.Replace(unifiedDSL, "{select: both, prefer: swap}", mode, 1)
		if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, dslText)); err != nil {
			t.Errorf("模式 %s 应能在统一数据集上解析：%v", mode, err)
		}
	}
}

func TestResolveUnifiedDatasetRejections(t *testing.T) {
	client := newUnifiedClient()
	noMarket := strings.Replace(unifiedDSL, "market: {select: both, prefer: swap}\n", "", 1)
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, noMarket)); err == nil || !strings.Contains(err.Error(), "必须写 market") {
		t.Fatalf("统一数据集上不写 market 应被拒绝：%v", err)
	}
	tuned := strings.Replace(strings.Replace(unifiedDSL, "{select: both, prefer: swap}", "{select: spot, prefer: spot}", 1), "rules:", "factor_params:\n  bias__var:\n    window: 20\nrules:", 1)
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, tuned)); err == nil || !strings.Contains(err.Error(), "factor_params") {
		t.Fatalf("统一数据集上的 factor_params 应被拒绝：%v", err)
	}
	// 统一数据集的任务已覆盖两个市场，不能再和别的任务一起绑定。
	dual := newDualClient()
	dual.datasets["ds_kline"] = client.datasets["ds_kline"]
	if _, _, err := Resolve(context.Background(), dual, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, unifiedDSL)); err == nil || !strings.Contains(err.Error(), "统一数据集") {
		t.Fatalf("统一数据集的任务不能与其他任务并存：%v", err)
	}
}

func TestCheckRegisteredMarketsUnified(t *testing.T) {
	marketOf := func(context.Context, string) (string, error) { return MarketMulti, nil }
	if err := CheckRegisteredMarkets(context.Background(), map[string]string{MarketMulti: "ftask_1h"}, marketOf); err != nil {
		t.Fatalf("统一数据集登记为 multi 应通过：%v", err)
	}
	if err := CheckRegisteredMarkets(context.Background(), map[string]string{"spot": "ftask_1h"}, marketOf); err == nil || !strings.Contains(err.Error(), "登记为 multi") {
		t.Fatalf("统一数据集登记成现货应被拒绝并提示 multi：%v", err)
	}
	spotOf := func(context.Context, string) (string, error) { return "spot", nil }
	if err := CheckRegisteredMarkets(context.Background(), map[string]string{MarketMulti: "ftask_1h"}, spotOf); err == nil {
		t.Fatal("单市场数据集登记成 multi 应被拒绝")
	}
}
