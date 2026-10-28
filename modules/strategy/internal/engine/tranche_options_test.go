package engine

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// 默认值与各选项（设计 3.1、3.2）：只做多、因子空值不参与排名、持有期按根数、每批份额固定。

func compileTranche(t *testing.T, body string, columns ...string) *dsl.Program {
	t.Helper()
	strategy, err := dsl.Parse([]byte("name: t\nrules:\n  - id: g\n    type: tranche\n    weight: 1\n" + body + "portfolio: {leverage: 10, max_missing: 1}\n"))
	require.NoError(t, err)
	program, err := dsl.Compile(strategy, append([]string{"close", "volume"}, columns...))
	require.NoError(t, err)
	return program
}

func trancheWeights(t *testing.T, program *dsl.Program, bars []map[string]map[string]float64) []map[string]string {
	t.Helper()
	var state State
	var out []map[string]string
	for i, bar := range bars {
		decision, err := Evaluate(program, trancheFrame(bar, int64(100+i), "g"), state)
		require.NoError(t, err)
		require.Equal(t, StatusOK, decision.Status, "第 %d 期：%v", i, decision.Summary.Notes)
		weights := map[string]string{}
		for _, target := range decision.Targets {
			weights[target.InstrumentID] = target.Weight.String()
		}
		out = append(out, weights)
		state = decision.State
	}
	return out
}

func trancheRow(close, volume, x float64) map[string]float64 {
	return map[string]float64{"close": close, "volume": volume, "x": x}
}

func TestTrancheGenericDefaults(t *testing.T) {
	// hold_period 必填。
	_, err := dsl.Parse([]byte("name: t\nrules:\n  - id: g\n    type: tranche\n    weight: 1\n    tranche:\n      long_factors: [{factor: x}]\n"))
	require.ErrorContains(t, err, "hold_period")

	// 每批份额 = 1 × 1 / h(2) / 1 = 0.5，不随价格变动；只做多。x 小者优先（descending: false）。
	program := compileTranche(t, "    tranche:\n      hold_period: 2\n      long_factors: [{factor: x, descending: false}]\n", "x")
	got := trancheWeights(t, program, []map[string]map[string]float64{
		{"A-USDT": trancheRow(10, 1, 1), "B-USDT": trancheRow(10, 1, 2)},
		{"A-USDT": trancheRow(20, 1, 2), "B-USDT": trancheRow(10, 1, 1)},
		{"A-USDT": trancheRow(30, 1, 2), "B-USDT": trancheRow(10, 1, 1)},
	})
	require.Equal(t, map[string]string{"A-USDT": "0.5"}, got[0])
	require.Equal(t, map[string]string{"A-USDT": "0.5", "B-USDT": "0.5"}, got[1]) // A 的价格翻倍，权重不变
	require.Equal(t, map[string]string{"B-USDT": "1"}, got[2])                    // A 持满 2 根到期，B 两批叠加
}

// hold_count: bars 下，标的中途没有候选行（volume = 0）也计根数；candidate_rows 下不计。
func TestTrancheHoldCountBarsVersusCandidateRows(t *testing.T) {
	bars := []map[string]map[string]float64{
		{"A-USDT": trancheRow(10, 1, 1), "B-USDT": trancheRow(10, 1, 2)},
		{"A-USDT": trancheRow(10, 0, 1), "B-USDT": trancheRow(10, 1, 9)}, // A 本期无成交
		{"A-USDT": trancheRow(10, 1, 9), "B-USDT": trancheRow(10, 1, 1)},
	}
	byBars := trancheWeights(t, compileTranche(t, "    tranche:\n      hold_period: 2\n      long_factors: [{factor: x, descending: false}]\n", "x"), bars)
	// 第 3 期 A 的第 1 批已持 3 根（> 2），不再有权重；第 2 期选中的 B 还在。
	require.NotContains(t, byBars[2], "A-USDT")
	byRows := trancheWeights(t, compileTranche(t, "    tranche:\n      hold_period: 2\n      hold_count: candidate_rows\n      long_factors: [{factor: x, descending: false}]\n", "x"), bars)
	// candidate_rows：A 第 2 期没有候选行不计数，第 3 期计为 2，仍有权重。
	require.Contains(t, byRows[2], "A-USDT")
}

// score.missing: drop 时因子空值的标的不参与排名；median 时补中位数名次后可能入选。
func TestTrancheScoreMissing(t *testing.T) {
	bars := []map[string]map[string]float64{{
		"A-USDT": trancheRow(10, 1, math.NaN()), "B-USDT": trancheRow(10, 1, 5), "C-USDT": trancheRow(10, 1, 1), "D-USDT": trancheRow(10, 1, 9),
	}}
	drop := trancheWeights(t, compileTranche(t, "    tranche:\n      hold_period: 1\n      long_coin_num: 2\n      long_factors: [{factor: x, descending: false}]\n", "x"), bars)
	require.Equal(t, []string{"B-USDT", "C-USDT"}, sortedKeysOf(drop[0]))
	median := trancheWeights(t, compileTranche(t, "    tranche:\n      hold_period: 1\n      long_coin_num: 2\n      score: {missing: median}\n      long_factors: [{factor: x, descending: false}]\n", "x"), bars)
	// 名次 C=1、B=2、D=3，A 补中位数 2：A 与 B 同分，按标的顺序 A 先。
	require.Equal(t, []string{"A-USDT", "C-USDT"}, sortedKeysOf(median[0]))
}

// candidates.require_volume: false 时不读 volume，volume = 0 的标的也是候选。
func TestTrancheRequireVolumeFalse(t *testing.T) {
	bars := []map[string]map[string]float64{{"A-USDT": trancheRow(10, 0, 1), "B-USDT": trancheRow(10, 1, 2)}}
	with := trancheWeights(t, compileTranche(t, "    tranche:\n      hold_period: 1\n      long_factors: [{factor: x, descending: false}]\n", "x"), bars)
	require.Equal(t, []string{"B-USDT"}, sortedKeysOf(with[0]))
	program := compileTranche(t, "    candidates: {require_volume: false}\n    tranche:\n      hold_period: 1\n      long_factors: [{factor: x, descending: false}]\n", "x")
	require.NotContains(t, program.Rules[0].Columns(), "volume")
	without := trancheWeights(t, program, bars)
	require.Equal(t, []string{"A-USDT"}, sortedKeysOf(without[0]))
}

// sizing: quantity 与 scale：份额 = 2 × 1 / 1 / 1 = 2，按数量持有时权重随价格变动。
func TestTrancheSizingQuantity(t *testing.T) {
	program := compileTranche(t, "    tranche:\n      hold_period: 2\n      sizing: {method: quantity, scale: 0.25}\n      long_factors: [{factor: x, descending: false}]\n", "x")
	got := trancheWeights(t, program, []map[string]map[string]float64{
		{"A-USDT": trancheRow(10, 1, 1), "B-USDT": trancheRow(10, 1, 2)},
		{"A-USDT": trancheRow(20, 1, 1), "B-USDT": trancheRow(10, 1, 2)},
	})
	require.Equal(t, "0.125", got[0]["A-USDT"])
	require.Equal(t, "0.375", got[1]["A-USDT"]) // 0.125 × 20/10 + 本期新开 0.125
}

func sortedKeysOf(m map[string]string) []string { return sortedStringKeys(m) }
