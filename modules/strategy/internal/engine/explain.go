package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 解释明细的阶段：一个标的在一条规则下最终停留的位置。
const (
	// StageAgedOut：不满足 min_age_bars，属于 G(r)。
	StageAgedOut = "aged_out"
	// StageMissing：无行、所需列缺失或上游因子对其失败，属于 M(r)。
	StageMissing = "missing"
	// StageFiltered：被 filter 淘汰。
	StageFiltered = "filtered"
	// StageScored：已打分但未被选中（含 select.where 淘汰）。
	StageScored = "scored"
	// StageIdle：signal 规则下未触发 entry，或 entry 与 exit 同时为真。
	StageIdle = "idle"
	// StageWeighted：进入目标权重。
	StageWeighted = "weighted"
	// StageDropped：曾被选中或持有但被剔除（filter_after、exit、score 无效）。
	StageDropped = "dropped"
	// StageHeld：因只缺上一根、所在市场本期不可用而缺数的已持仓标的，持仓数量保持不动（不求值、不调仓），
	// 原因分别是 prev_hold、market_hold。
	StageHeld = "held"
)

// Item 是一条规则下一个标的的解释：到达的阶段、分数、名次、权重与原因。
type Item struct {
	RuleID       string `json:"rule_id"`
	InstrumentID string `json:"instrument_id"`
	// Market 是标的所在的市场：入选的权重取它进入的市场，其余取所选行所在的市场；没有行的冻结标的取上期持仓所在的市场。
	Market string `json:"market,omitempty"`
	Stage  string `json:"stage"`
	Score  string `json:"score,omitempty"`
	Rank   int    `json:"rank,omitempty"`
	Weight string `json:"weight,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Scale 是乘在输出权重上的仓位系数（截断之后的值）；Weight 是乘系数之后的权重。规则没写 scale 时为空。
	Scale string `json:"scale,omitempty"`
}

// sortItems 按规则声明顺序、标的 id 排序，保证解释明细可重复。
func sortItems(program *dsl.Program, items []Item) {
	order := make(map[string]int, len(program.Rules))
	for i := range program.Rules {
		order[program.Rules[i].Rule.ID] = i
	}
	sort.SliceStable(items, func(i, j int) bool {
		if order[items[i].RuleID] != order[items[j].RuleID] {
			return order[items[i].RuleID] < order[items[j].RuleID]
		}
		return items[i].InstrumentID < items[j].InstrumentID
	})
}
