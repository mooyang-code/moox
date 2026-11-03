package runtime

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestRequiredTurnoverSumsTargetChanges(t *testing.T) {
	since := int64(1_000)
	receipts := []store.TargetReceiptRecord{
		// 窗口前的基准：BTC 1、ETH 10。
		{AcceptedAt: 900, QuantityTargetsJSON: `[{"instrument_id":"BTC","quantity":"1"},{"instrument_id":"ETH","quantity":"10"}]`,
			ReferencePricesJSON: `{"BTC":"100","ETH":"10"}`},
		// BTC 加到 3（变化 2×100），ETH 保持。
		{AcceptedAt: 1_100, QuantityTargetsJSON: `[{"instrument_id":"BTC","quantity":"3"},{"instrument_id":"ETH","hold":true}]`,
			ReferencePricesJSON: `{"BTC":"100"}`},
		// ETH 不再列出即目标为 0（变化 10×10，沿用上次参考价），BTC 不变。
		{AcceptedAt: 1_200, QuantityTargetsJSON: `[{"instrument_id":"BTC","quantity":"3"}]`,
			ReferencePricesJSON: `{"BTC":"100"}`},
	}
	required, err := requiredTurnover(receipts, since)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(required-300) > 1e-9 {
		t.Fatalf("required = %v, want 300", required)
	}
	// 没有窗口前基准时，第一个目标与空仓比较。
	required, err = requiredTurnover(receipts[1:], since)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(required-300) > 1e-9 { // BTC 0→3 计 300，ETH 保持没有已知数量计 0
		t.Fatalf("required without baseline = %v, want 300", required)
	}
}
