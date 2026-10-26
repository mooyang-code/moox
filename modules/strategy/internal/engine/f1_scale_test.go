package engine

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// fh003Shape 生成与 FH003 同等规模的 f1 规则（合成参数）：32 个选币因子列 × 13 个过滤窗口 × 8 种持有期 × 若干阈值，共 1960 个子策略。
func fh003Shape() (string, []string) {
	holds := []int{1, 2, 3, 4, 6, 12, 24, 36}
	var rows strings.Builder
	count := 0
	for i := 0; count < 1960; i++ {
		fmt.Fprintf(&rows, "        - [%d, %d, %d, %g, %g]\n", holds[i%len(holds)], i%32, (i/32)%13, 0.05+0.025*float64(i%10), 0.0005)
		count++
	}
	text := `name: fh003_shape
rules:
  - id: fh003
    type: f1
    weight: {total: 1}
    f1:
      hold_period: "{h}"
      seal999: false
      long_factors: [{factor: "po__{n1}", descending: true, weight: 1}]
      short_factors: []
      long_weight: 2
      short_weight: 0
      long_coin_num: 1
      short_coin_num: 0
      filter_after:
        - {direction: long, factor: "us__{n2}", type: value, op: lte, value: "{p2}"}
      stg_weight: "{w}"
    params:
      columns: [h, n1, n2, p2, w]
      rows:
` + rows.String() + "portfolio: {leverage: 2}\n"
	columns := []string{"close", "volume"}
	for i := 0; i < 32; i++ {
		columns = append(columns, fmt.Sprintf("po__%d", i))
	}
	for i := 0; i < 13; i++ {
		columns = append(columns, fmt.Sprintf("us__%d", i))
	}
	return text, columns
}

type f1ScaleRun struct {
	rule   *dsl.CompiledRule
	ids    []string
	closes []float64
	rng    *rand.Rand
	cols   []string
}

func newF1ScaleRun(t testing.TB) *f1ScaleRun {
	text, columns := fh003Shape()
	strategy, err := dsl.Parse([]byte(text))
	require.NoError(t, err)
	program, err := dsl.Compile(strategy, columns)
	require.NoError(t, err)
	require.Len(t, program.Rules[0].F1Groups, 32)
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("C%03d-USDT", i)
	}
	sort.Strings(ids)
	run := &f1ScaleRun{rule: &program.Rules[0], ids: ids, closes: make([]float64, len(ids)), rng: rand.New(rand.NewSource(1)), cols: columns}
	for i := range run.closes {
		run.closes[i] = 1 + run.rng.Float64()*100
	}
	return run
}

// frame 生成一期数据：因子值有持续性（每期小幅变化），1% 的标的本期没有成交。
func (r *f1ScaleRun) frame(bar int, previous map[string]map[string]float64) (Frame, map[string]map[string]float64) {
	return r.frameWithGaps(bar, previous, nil)
}

// frameWithGaps 同 frame，idle 里的标的本期没有成交（模拟长期缺行）。
func (r *f1ScaleRun) frameWithGaps(bar int, previous map[string]map[string]float64, idle map[string]bool) (Frame, map[string]map[string]float64) {
	rows := make(map[string]Row, len(r.ids))
	values := make(map[string]map[string]float64, len(r.ids))
	for i, id := range r.ids {
		r.closes[i] *= 1 + r.rng.NormFloat64()*0.003
		current := map[string]float64{"close": r.closes[i], "volume": 1 + r.rng.Float64()}
		if r.rng.Float64() < 0.01 || idle[id] {
			current["volume"] = 0
		}
		for _, column := range r.cols[2:] {
			base := r.rng.Float64() * 0.2
			if old, ok := previous[id][column]; ok {
				base = old + r.rng.NormFloat64()*0.005
			}
			current[column] = base
		}
		values[id] = current
		rows[id] = Row{Values: current}
	}
	return Frame{BarIndex: int64(bar), Rows: rows, Universe: r.ids, Expected: map[string][]string{r.rule.Rule.ID: r.ids}, AgedOut: map[string][]string{}}, values
}

// 稳态状态体积：FH003 规模下跑 120 期（超过最长持有期 36 根），状态 JSON 不超过 256 KB。
func TestF1StateSizeAtFH003Scale(t *testing.T) {
	run := newF1ScaleRun(t)
	var state RuleState
	var previous map[string]map[string]float64
	largest := 0
	for bar := 0; bar < 120; bar++ {
		frame, values := run.frame(bar, previous)
		previous = values
		result, err := evaluateF1(run.rule, frame, partitionF1(run.rule, frame, state.Held), state, noise)
		require.NoError(t, err)
		require.Empty(t, result.notes)
		state = result.state
		raw, err := json.Marshal(state)
		require.NoError(t, err)
		largest = max(largest, len(raw))
	}
	require.LessOrEqual(t, largest, f1StateNoteBytes)
	t.Logf("FH003 规模稳态状态最大 %d 字节", largest)
}

// 多个币先后被选中、又轮流长期没有成交：开仓记录随之保留，状态体积仍在观察阈值内，明细给出 volume_zero。
func TestF1StateSizeWithRotatingGaps(t *testing.T) {
	run := newF1ScaleRun(t)
	var state RuleState
	var previous map[string]map[string]float64
	idle := map[string]bool{}
	largest := 0
	for bar := 0; bar < 240; bar++ {
		// 每 10 期把上一期持仓里的前 5 个币设为接下来 80 期都没有成交。
		if bar%10 == 0 {
			for i, id := range state.Held {
				if i >= 5 {
					break
				}
				idle[id] = true
			}
		}
		if bar%80 == 0 && bar > 0 {
			idle = map[string]bool{}
		}
		frame, values := run.frameWithGaps(bar, previous, idle)
		previous = values
		sets := partitionF1(run.rule, frame, state.Held)
		for id := range idle {
			require.NotContains(t, sets.available, id)
		}
		result, err := evaluateF1(run.rule, frame, sets, state, noise)
		require.NoError(t, err)
		state = result.state
		raw, err := json.Marshal(state)
		require.NoError(t, err)
		largest = max(largest, len(raw))
		if len(idle) > 0 {
			found := false
			for _, item := range sets.items(run.rule.Rule.ID) {
				if item.Reason == reasonVolumeZero {
					found = true
				}
			}
			require.True(t, found)
		}
	}
	require.LessOrEqual(t, largest, f1StateNoteBytes)
	t.Logf("轮流长期缺行时状态最大 %d 字节", largest)
}

// BenchmarkF1EvaluateFH003Scale：FH003 规模的单期求值（含状态读写）。本机实测约 25 ms/期。
func BenchmarkF1EvaluateFH003Scale(b *testing.B) {
	run := newF1ScaleRun(b)
	var state RuleState
	var previous map[string]map[string]float64
	frames := make([]Frame, 60)
	for i := range frames {
		frames[i], previous = run.frame(i, previous)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame := frames[i%len(frames)]
		frame.BarIndex = int64(i)
		result, err := evaluateF1(run.rule, frame, partitionF1(run.rule, frame, state.Held), state, noise)
		if err != nil {
			b.Fatal(err)
		}
		raw, _ := json.Marshal(result.state)
		state = RuleState{}
		_ = json.Unmarshal(raw, &state)
	}
}
