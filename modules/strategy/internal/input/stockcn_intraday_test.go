package input

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// A 股（market_type=equity）按现货处理：杠杆不大于 1、不许做空。
func TestResolveEquityFollowsSpotLimits(t *testing.T) {
	client := newFakeClient("equity")
	client.tagMarkets["binance_equity"] = "equity"
	resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, exampleDSL))
	if err != nil || resolved.Market.Select != "equity" {
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
	// 数据集属性直接声明 equity 也接受。
	source := client.datasets["ds_kline"]
	source.Attributes = map[string]string{"market_type": "equity"}
	source.SubjectTags = nil
	client.datasets["ds_kline"] = source
	if resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, exampleDSL)); err != nil || resolved.Market.Select != "equity" {
		t.Fatalf("数据集属性 market_type=equity 应按现货解析：%+v err=%v", resolved, err)
	}
}

// A 股 5m 的因子结果 View 能解析：日历继承自源数据集，按交易时段对齐。
func TestResolveCNStockIntradayView(t *testing.T) {
	client := newFakeClient("spot")
	client.views["dataset_factor_cn_5m"] = upstream.ViewInfo{DatasetID: "dataset_factor_cn_5m", Frequency: "5m", Status: "active", ActiveIndexID: "idx_cn", Columns: []upstream.ViewColumn{plainColumn("close")}}
	client.datasets["dataset_factor_cn_5m"] = upstream.DatasetInfo{DatasetID: "dataset_factor_cn_5m", Status: "active", Frequency: "5m", Retention: "7200h", Attributes: map[string]string{"source_dataset_id": "ds_cn_kline", "dataset_role": "factor_result"}}
	client.datasets["ds_cn_kline"] = upstream.DatasetInfo{DatasetID: "ds_cn_kline", Status: "active", Frequency: "5m", Retention: "7200h", Alignment: "session", Attributes: map[string]string{"calendar": "cn_stock", "market_type": "equity"}}
	strategy := parseStrategy(t, "name: cn_5m\nuniverse: {min_age_bars: 9600}\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n")
	resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_cn_5m"}, strategy)
	if err != nil {
		t.Fatalf("A 股 5m View 应能解析：%v", err)
	}
	if resolved.Calendar != "cn_stock" || resolved.Bar != "5m" || resolved.Market.Select != "equity" {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	strategy = parseStrategy(t, "name: cn_5m\nuniverse: {min_age_bars: 9601}\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n")
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_cn_5m"}, strategy); err == nil || !strings.Contains(err.Error(), "9600") {
		t.Fatalf("min_age_bars 超过按交易时段折算的保留根数应拒绝：%v", err)
	}
}
