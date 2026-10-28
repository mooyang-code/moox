package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// 等价基准（设计 3.4）：重构前用旧 f1 求值生成每期的目标权重与开仓记录，重构后用 tranche + preset: f1 逐期比较。
// 基准文件不进仓库，用 TRANCHE_BASELINE_IN 指定；没有设置时跳过。

type trancheBaselineBar struct {
	Rows    map[string]map[string]*float64 `json:"rows"`
	Targets map[string]string              `json:"targets"`
	Status  string                         `json:"status"`
	Groups  []trancheBaselineGroup         `json:"groups"`
}

type trancheBaselineGroup struct {
	Subs  []int         `json:"subs"`
	Picks []TranchePick `json:"picks"`
}

type trancheBaselineScenario struct {
	Name    string               `json:"name"`
	DSL     string               `json:"dsl"`
	Columns []string             `json:"columns"`
	Bars    []trancheBaselineBar `json:"bars"`
}

var (
	legacyTypePattern = regexp.MustCompile(`type: f1\n([ \t]*)`)
	legacyKeyPattern  = regexp.MustCompile(`\n([ \t]+)f1:`)
	legacyCalcPattern = regexp.MustCompile(`calc_factor_type: "?(cross|vertical)"?`)
)

// legacyToTranche 把旧 f1 写法改成 tranche + preset: f1。
func legacyToTranche(text string) string {
	text = legacyTypePattern.ReplaceAllString(text, "type: tranche\n${1}preset: f1\n${1}")
	text = legacyKeyPattern.ReplaceAllString(text, "\n${1}tranche:")
	return legacyCalcPattern.ReplaceAllStringFunc(text, func(match string) string {
		if strings.Contains(match, "vertical") {
			return "score: {method: raw_sum}"
		}
		return "score: {method: rank_sum}"
	})
}

func TestTrancheMatchesLegacyBaseline(t *testing.T) {
	path := os.Getenv("TRANCHE_BASELINE_IN")
	if path == "" {
		t.Skip("未设置 TRANCHE_BASELINE_IN")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var scenarios []trancheBaselineScenario
	require.NoError(t, json.Unmarshal(raw, &scenarios))
	require.NotEmpty(t, scenarios)
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			strategy, err := dsl.Parse([]byte(legacyToTranche(scenario.DSL)))
			require.NoError(t, err)
			program, err := dsl.Compile(strategy, scenario.Columns)
			require.NoError(t, err)
			rule := &program.Rules[0]
			subsOf := map[string]string{}
			for _, group := range rule.TrancheGroups {
				subsOf[group.StateKey] = fmt.Sprint(group.Subs)
			}
			var state State
			for i, bar := range scenario.Bars {
				rows := make(map[string]map[string]float64, len(bar.Rows))
				for id, values := range bar.Rows {
					decoded := make(map[string]float64, len(values))
					for column, value := range values {
						if value == nil {
							decoded[column] = math.NaN()
						} else {
							decoded[column] = *value
						}
					}
					rows[id] = decoded
				}
				decision, err := Evaluate(program, trancheFrame(rows, int64(i), rule.Rule.ID), state)
				require.NoError(t, err)
				require.Equal(t, bar.Status, decision.Status, "第 %d 期", i)
				got := map[string]string{}
				for _, target := range decision.Targets {
					got[target.InstrumentID] = target.Weight.String()
				}
				require.Equal(t, bar.Targets, got, "第 %d 期目标", i)
				if decision.Status == StatusOK {
					encoded, err := json.Marshal(decision.State)
					require.NoError(t, err)
					state = State{}
					require.NoError(t, json.Unmarshal(encoded, &state))
				}
				var groups []trancheBaselineGroup
				if tranche := state.Rules[rule.Rule.ID].Tranche; tranche != nil {
					for _, group := range tranche.Groups {
						var subs []int
						require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(subsOf[group.Key], " ", ",")), &subs))
						groups = append(groups, trancheBaselineGroup{Subs: subs, Picks: group.Picks})
					}
				}
				sort.Slice(groups, func(a, b int) bool { return fmt.Sprint(groups[a].Subs) < fmt.Sprint(groups[b].Subs) })
				want, err := json.Marshal(bar.Groups)
				require.NoError(t, err)
				have, err := json.Marshal(groups)
				require.NoError(t, err)
				require.JSONEq(t, string(want), string(have), "第 %d 期开仓记录", i)
			}
		})
	}
}
