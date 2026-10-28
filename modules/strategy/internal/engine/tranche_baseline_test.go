package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// 等价基准（设计 3.4）：重构前用旧实现生成每期的目标权重与开仓记录，重构后用显式写出参考选项的 tranche 规则逐期比较。
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

// legacyToTranche 把基准里的旧写法（type: f1，默认值按参考实现）改成显式写出全部选项的 tranche 规则。
func legacyToTranche(t *testing.T, text string) string {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
	root := doc.Content[0]
	if rules := yamlValue(root, "rules"); rules != nil {
		for _, rule := range rules.Content {
			if kind := yamlValue(rule, "type"); kind != nil && (kind.Value == "f1" || kind.Value == "tranche") {
				kind.Value = "tranche"
				yamlDelete(rule, "preset")
				for i := 0; i+1 < len(rule.Content); i += 2 {
					if rule.Content[i].Value == "f1" {
						rule.Content[i].Value = "tranche"
					}
				}
				if template := yamlValue(rule, "tranche"); template != nil {
					explicitReferenceOptions(template)
				}
			}
		}
	}
	raw, err := yaml.Marshal(root)
	require.NoError(t, err)
	return string(raw)
}

// explicitReferenceOptions 写出参考实现的默认值：持有 12 期、按候选行计数、空值补中位数名次、按数量持有（份额系数 0.5）、
// 多空权重与个数都为 1；过滤条件 op 默认 lt、value 默认 0.2，df1/df2 写成 long/short。
func explicitReferenceOptions(template *yaml.Node) {
	yamlDelete(template, "seal999")
	method := "rank_sum"
	if calc := yamlValue(template, "calc_factor_type"); calc != nil {
		if strings.Trim(calc.Value, `"`) == "vertical" {
			method = "raw_sum"
		}
		yamlDelete(template, "calc_factor_type")
	}
	if score := yamlValue(template, "score"); score != nil {
		if m := yamlValue(score, "method"); m != nil {
			method = m.Value
		}
		yamlDelete(template, "score")
	}
	defaults := []struct{ key, value string }{
		{"hold_period", "12"}, {"long_weight", "1"}, {"short_weight", "1"}, {"long_coin_num", "1"}, {"short_coin_num", "1"},
	}
	for _, d := range defaults {
		if yamlValue(template, d.key) == nil {
			yamlSet(template, d.key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: d.value})
		}
	}
	yamlSet(template, "hold_count", &yaml.Node{Kind: yaml.ScalarNode, Value: "candidate_rows"})
	yamlSet(template, "score", yamlMap("method", method, "missing", "median"))
	yamlSet(template, "sizing", yamlMap("method", "quantity", "scale", "0.5"))
	for _, key := range []string{"filter_before", "filter_after"} {
		if filters := yamlValue(template, key); filters != nil {
			explicitFilters(filters)
		}
	}
}

func explicitFilters(list *yaml.Node) {
	for _, filter := range list.Content {
		if items := yamlValue(filter, "items"); items != nil {
			explicitFilters(items)
			continue
		}
		if direction := yamlValue(filter, "direction"); direction != nil {
			switch direction.Value {
			case "df1":
				direction.Value = "long"
			case "df2":
				direction.Value = "short"
			}
		}
		if yamlValue(filter, "op") == nil {
			yamlSet(filter, "op", &yaml.Node{Kind: yaml.ScalarNode, Value: "lt"})
		}
		if yamlValue(filter, "value") == nil {
			yamlSet(filter, "value", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: "0.2"})
		}
	}
}

func yamlValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func yamlDelete(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func yamlSet(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

func yamlMap(pairs ...string) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	for i := 0; i+1 < len(pairs); i += 2 {
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: pairs[i]}, &yaml.Node{Kind: yaml.ScalarNode, Value: pairs[i+1]})
	}
	return out
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
			strategy, err := dsl.Parse([]byte(legacyToTranche(t, scenario.DSL)))
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
