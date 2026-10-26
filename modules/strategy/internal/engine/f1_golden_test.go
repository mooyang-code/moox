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

// 黄金用例：期望值由 alphalib F1 框架（alphalib-dev 的 F1Strategy / F1MultiStrategy 与 f1_utils）在合成数据上算出，
// 生成脚本不在仓库里（它运行第三方代码）；用例只含合成数据与合成参数。见 docs/计划/F1策略迁移/F1策略迁移执行计划.md P4。

type f1Golden struct {
	Name     string               `json:"name"`
	DSL      string               `json:"dsl"`
	Expected []map[string]float64 `json:"expected"`
}

func loadF1Bars(t *testing.T) []map[string]map[string]float64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "f1", "bars.json"))
	require.NoError(t, err)
	var bars []map[string]map[string]float64
	require.NoError(t, json.Unmarshal(raw, &bars))
	return bars
}

func f1Frame(bar map[string]map[string]float64, index int64, ruleID string) Frame {
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

func TestF1MatchesAlphalibGolden(t *testing.T) {
	bars := loadF1Bars(t)
	columns := columnsOf(bars)
	files, err := filepath.Glob(filepath.Join("testdata", "f1", "*.json"))
	require.NoError(t, err)
	cases := 0
	for _, path := range files {
		base := filepath.Base(path)
		if base == "bars.json" || strings.HasPrefix(base, "logic_") {
			continue
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var golden f1Golden
		require.NoError(t, json.Unmarshal(raw, &golden))
		t.Run(golden.Name, func(t *testing.T) {
			strategy, err := dsl.Parse([]byte(golden.DSL))
			require.NoError(t, err)
			program, err := dsl.Compile(strategy, columns)
			require.NoError(t, err)
			rule := &program.Rules[0]
			var state RuleState
			for i, bar := range bars {
				frame := f1Frame(bar, int64(i), rule.Rule.ID)
				sets := partitionF1(rule, frame, state.Held)
				result, err := evaluateF1(rule, frame, sets, state, noise)
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
						t.Fatalf("第 %d 期 %s：moox %.15g，F1 %.15g", i, id, got, want[id])
					}
				}
			}
		})
		cases++
	}
	require.GreaterOrEqual(t, cases, 12)
}

// 逻辑组合：F1Strategy 不接受元组写法，语义来自 f1_utils.filter_generate；期望值是它在每期样本上保留的标的。
func TestF1LogicGroupMatchesF1Utils(t *testing.T) {
	bars := loadF1Bars(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "f1", "logic_group_filter.json"))
	require.NoError(t, err)
	var golden struct {
		Logic string     `json:"logic"`
		Kept  [][]string `json:"kept"`
	}
	require.NoError(t, json.Unmarshal(raw, &golden))
	strategy, err := dsl.Parse([]byte(`name: g
rules:
  - id: g
    type: f1
    weight: 1
    f1:
      seal999: false
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
	filter := strategy.Rules[0].F1.Subs[0].FilterBefore[0]
	for i, bar := range bars {
		frame := f1Frame(bar, int64(i), "g")
		ids := append([]string(nil), frame.Universe...)
		sort.SliceStable(ids, func(a, b int) bool { return f1Key(ids[a]) < f1Key(ids[b]) })
		sample := f1Sample{ids: ids, frame: frame}
		match := f1Match(ids, func(column string) func(id string) float64 {
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
