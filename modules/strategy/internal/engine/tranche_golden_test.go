package engine

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// 黄金用例：期望值由一个外部参考实现（pandas 写的批次持仓选币框架）在合成数据上算出，生成脚本不在仓库里；
// 用例只含合成数据与合成参数，DSL 显式写出参考实现的选项（按候选行计数、空值补中位数名次、按数量持有、份额系数 0.5）。

type trancheGolden struct {
	Name     string               `json:"name"`
	DSL      string               `json:"dsl"`
	Expected []map[string]float64 `json:"expected"`
}

func loadTrancheBars(t *testing.T) []map[string]map[string]float64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "reference", "bars.json"))
	require.NoError(t, err)
	var bars []map[string]map[string]float64
	require.NoError(t, json.Unmarshal(raw, &bars))
	return bars
}

func trancheFrame(bar map[string]map[string]float64, index int64, ruleID string) Frame {
	ids := make([]string, 0, len(bar))
	rows := make(map[string]Row, len(bar))
	for id, values := range bar {
		ids = append(ids, id)
		rows[id] = Row{Values: values}
	}
	sort.Strings(ids)
	return Frame{BarIndex: index, Rows: rows, Universe: ids, Expected: map[string][]string{ruleID: ids}, AgedOut: map[string][]string{}}
}

func columnsOf(bars []map[string]map[string]float64) []string {
	set := map[string]struct{}{}
	for _, bar := range bars {
		for _, values := range bar {
			for column := range values {
				set[column] = struct{}{}
			}
		}
	}
	columns := make([]string, 0, len(set))
	for column := range set {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	return columns
}

func TestTrancheMatchesReferenceGolden(t *testing.T) {
	bars := loadTrancheBars(t)
	columns := columnsOf(bars)
	files, err := filepath.Glob(filepath.Join("testdata", "reference", "*.json"))
	require.NoError(t, err)
	cases := 0
	for _, path := range files {
		base := filepath.Base(path)
		if base == "bars.json" || strings.HasPrefix(base, "logic_") {
			continue
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var golden trancheGolden
		require.NoError(t, json.Unmarshal(raw, &golden))
		t.Run(golden.Name, func(t *testing.T) {
			strategy, err := dsl.Parse([]byte(golden.DSL))
			require.NoError(t, err)
			program, err := dsl.Compile(strategy, columns)
			require.NoError(t, err)
			rule := &program.Rules[0]
			var state RuleState
			for i, bar := range bars {
				frame := trancheFrame(bar, int64(i), rule.Rule.ID)
				sets := partitionTranche(rule, frame, state.Held)
				result, err := evaluateTranche(rule, frame, sets, state, noise)
				require.NoError(t, err)
				state = result.state
				want := golden.Expected[i]
				ids := map[string]struct{}{}
				for id := range want {
					ids[id] = struct{}{}
				}
				for id := range result.weights {
					ids[id] = struct{}{}
				}
				for id := range ids {
					got := 0.0
					if weight, ok := result.weights[id]; ok {
						got, _ = weight.Rat().Float64()
					}
					if math.Abs(got-want[id]) > 1e-12 {
						t.Fatalf("第 %d 期 %s：moox %.15g，参考 %.15g", i, id, got, want[id])
					}
				}
			}
		})
		cases++
	}
	require.GreaterOrEqual(t, cases, 12)
}

// 逻辑组合：期望值是参考实现在每期样本上保留的标的。
func TestTrancheLogicGroupMatchesReference(t *testing.T) {
	bars := loadTrancheBars(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "reference", "logic_group_filter.json"))
	require.NoError(t, err)
	var golden struct {
		Logic string     `json:"logic"`
		Kept  [][]string `json:"kept"`
	}
	require.NoError(t, json.Unmarshal(raw, &golden))
	strategy, err := dsl.Parse([]byte(`name: g
rules:
  - id: g
    type: tranche
    weight: 1
    tranche:
      hold_period: 12
      hold_count: candidate_rows
      score: {method: rank_sum, missing: median}
      sizing: {method: quantity, scale: 0.5}
      long_factors: [{factor: x1}]
      short_weight: 0
      filter_before:
        - logic: "` + golden.Logic + `"
          items:
            - {direction: long, factor: y1, type: value, op: lte, value: 0.9}
            - {direction: long, factor: y2, type: rank, op: lte, value: 4}
            - {direction: long, factor: y3, type: pct, op: gte, value: 0.6}
`))
	require.NoError(t, err)
	filter := strategy.Rules[0].Tranche.Subs[0].FilterBefore[0]
	for i, bar := range bars {
		frame := trancheFrame(bar, int64(i), "g")
		ids := append([]string(nil), frame.Universe...)
		sort.SliceStable(ids, func(a, b int) bool { return trancheKey(ids[a]) < trancheKey(ids[b]) })
		sample := trancheSample{ids: ids, frame: frame}
		match := trancheMatch(ids, func(column string) func(id string) float64 {
			return func(id string) float64 { return sample.value(id, column) }
		}, filter)
		var kept []string
		for k, id := range ids {
			if match[k] {
				kept = append(kept, id)
			}
		}
		sort.Strings(kept)
		want := golden.Kept[i]
		if len(want) == 0 {
			want = nil
		}
		require.Equal(t, want, kept, "第 %d 期", i)
	}
}
