package dsl

import (
	"os"
	"path/filepath"
	"testing"
)

// setup start 给各空间建的默认示例策略（modules/cli 的 spaceExamples 引用这些文件）都要能通过解析与校验。
func TestDefaultExampleStrategiesParse(t *testing.T) {
	for _, name := range []string{"dual_ma_swap_1m.yaml", "dual_ma_cn_5m.yaml", "dual_ma_hk_5m.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "examples", "strategies", name))
		if err != nil {
			t.Fatal(err)
		}
		strategy, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s 应能通过校验：%v", name, err)
		}
		if len(strategy.Rules) != 1 {
			t.Fatalf("%s 应只有一条规则：%d", name, len(strategy.Rules))
		}
	}
}

// 港股默认策略可以直接挂模拟盘：分批换仓（不是每根全体重选）、预算留出 2% 现金、单只权重不随选中个数变化。
func TestHKDefaultStrategyIsLowTurnover(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "examples", "strategies", "dual_ma_hk_5m.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	rule := strategy.Rules[0]
	if strategy.Bar != "5m" || rule.Holding == nil || rule.Holding.Bars != 12 || len(rule.Holding.Offsets) != 2 {
		t.Fatalf("应是 5m、每 12 根重选、两个批次：bar=%s holding=%+v", strategy.Bar, rule.Holding)
	}
	if rule.Weight.Total.String() != "0.98" || rule.Weight.Cap.String() != "0.05" {
		t.Fatalf("预算 0.98（留 2%% 现金）、单只上限 0.05：%+v", rule.Weight)
	}
}
