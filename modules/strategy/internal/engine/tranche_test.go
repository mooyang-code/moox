package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/stretchr/testify/require"
)

func loadGoldenCase(t *testing.T, name string) trancheGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "reference", name+".json"))
	require.NoError(t, err)
	var golden trancheGolden
	require.NoError(t, json.Unmarshal(raw, &golden))
	return golden
}

// 每期的状态经 JSON 写入、读回再传给下一期（相当于每期重启进程），结果与不经序列化完全相同。
func TestTrancheStateSurvivesRestart(t *testing.T) {
	bars := loadTrancheBars(t)
	golden := loadGoldenCase(t, "multi_sub_merge")
	strategy, err := dsl.Parse([]byte(golden.DSL))
	require.NoError(t, err)
	program, err := dsl.Compile(strategy, columnsOf(bars))
	require.NoError(t, err)
	rule := &program.Rules[0]
	var direct, restarted RuleState
	for i, bar := range bars {
		frame := trancheFrame(bar, int64(i), rule.Rule.ID)
		a, err := evaluateTranche(rule, frame, partitionTranche(rule, frame, direct.Held), direct, noise)
		require.NoError(t, err)
		b, err := evaluateTranche(rule, frame, partitionTranche(rule, frame, restarted.Held), restarted, noise)
		require.NoError(t, err)
		require.Equal(t, len(a.weights), len(b.weights), "第 %d 期", i)
		for id, w := range a.weights {
			require.Zero(t, w.Cmp(b.weights[id]), "第 %d 期 %s", i, id)
		}
		direct = a.state
		raw, err := json.Marshal(b.state)
		require.NoError(t, err)
		restarted = RuleState{}
		require.NoError(t, json.Unmarshal(raw, &restarted))
	}
	// 状态只保留仍可能有贡献的开仓记录：组内最大 h 为 36，每组记录数不超过 36 × 选币数。
	for _, group := range direct.Tranche.Groups {
		require.LessOrEqual(t, len(group.Picks), 36)
	}
}

// 完整求值：守门、组合层截断到 1e-4、按数量持有让 Σw 略大于 1 时 leverage 上限只作约束不缩放。
func TestTrancheEvaluateEndToEnd(t *testing.T) {
	bars := loadTrancheBars(t)
	golden := loadGoldenCase(t, "hold_36_volume_gap")
	strategy, err := dsl.Parse([]byte(golden.DSL))
	require.NoError(t, err)
	program, err := dsl.Compile(strategy, columnsOf(bars))
	require.NoError(t, err)
	var state State
	for i, bar := range bars {
		decision, err := Evaluate(program, trancheFrame(bar, int64(i), "g"), state)
		require.NoError(t, err)
		require.Equal(t, StatusOK, decision.Status, "第 %d 期：%v", i, decision.Summary.Notes)
		got := map[string]quant.Decimal{}
		for _, target := range decision.Targets {
			got[target.InstrumentID] = target.Weight
		}
		for id, want := range golden.Expected[i] {
			truncated := quant.Must(formatFloat(want)).TruncateTo(WeightPlaces)
			require.Zero(t, got[id].Cmp(truncated), "第 %d 期 %s：%s ≠ %s", i, id, got[id].String(), truncated.String())
		}
		for _, item := range decision.Items {
			require.Contains(t, []string{StageWeighted, StageDropped, StageMissing, StageIdle}, item.Stage)
			if item.Stage == StageIdle {
				require.Equal(t, reasonVolumeZero, item.Reason)
			}
		}
		state = decision.State
	}
	// 缺数守门：一半标的没有行时整期跳过（max_missing 设为 0）。
	strict, err := dsl.Parse([]byte(golden.DSL[:len(golden.DSL)-len("  max_missing: 1\n")] + "  max_missing: 0\n"))
	require.NoError(t, err)
	program, err = dsl.Compile(strict, columnsOf(bars))
	require.NoError(t, err)
	frame := trancheFrame(bars[0], 0, "g")
	delete(frame.Rows, "AAA-USDT")
	decision, err := Evaluate(program, frame, State{})
	require.NoError(t, err)
	require.Equal(t, StatusSkipped, decision.Status)
	require.Equal(t, SkipTooManyMissing, decision.SkipReason)
}

func formatFloat(v float64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
