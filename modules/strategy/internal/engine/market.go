package engine

import (
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 本文件实现多市场的选币池构造与定市场（设计 3.1）。规则内部仍按标的计算：这里只负责
// ① 为每个标的在各市场的行里选出一行（连同两个市场的可用标记），② 给规则贡献的权重定下市场并按（标的, 市场）作键。

// targetKeySeparator 分隔 State.Targets 键里的标的 ID 与市场；标的 ID 不含 "|"。
const targetKeySeparator = "|"

// TargetKey 返回目标在 State.Targets 里的键：标的 ID 加市场。同一个币的现货与永续是两个目标。
func TargetKey(instrumentID, market string) string {
	return instrumentID + targetKeySeparator + market
}

// SplitTargetKey 把 State.Targets 的键拆回标的 ID 与市场。
func SplitTargetKey(key string) (instrumentID, market string) {
	if i := strings.LastIndex(key, targetKeySeparator); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// marketRank 是排序时市场的先后：现货在前，永续其次，股票最后。
func marketRank(market string) int {
	switch market {
	case dsl.MarketSpot:
		return 0
	case dsl.MarketSwap:
		return 1
	default:
		return 2
	}
}

// lessKey 先比较标的，再比较市场（现货在前），单市场策略的输出顺序与按标的排序一致。
func lessKey(a, b string) bool {
	idA, marketA := SplitTargetKey(a)
	idB, marketB := SplitTargetKey(b)
	if idA != idB {
		return idA < idB
	}
	return marketRank(marketA) < marketRank(marketB)
}

func sortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool { return lessKey(keys[i], keys[j]) })
}

// heldEntry 是上期目标里某个标的在一个市场的非零权重。
type heldEntry struct {
	market string
	weight quant.Decimal
}

// heldIndex 按标的归并上期 State.Targets 的非零项（各标的内部按市场先后排序）。
type heldIndex map[string][]heldEntry

func indexHeld(previous State) heldIndex {
	index := make(heldIndex)
	for key, raw := range previous.Targets {
		weight, err := quant.Parse(raw)
		if err != nil || weight.IsZero() {
			continue
		}
		id, market := SplitTargetKey(key)
		index[id] = append(index[id], heldEntry{market: market, weight: weight})
	}
	for _, entries := range index {
		sort.Slice(entries, func(i, j int) bool { return marketRank(entries[i].market) < marketRank(entries[j].market) })
	}
	return index
}

// subjects 返回上期持有非零目标的标的（排序）。
func (h heldIndex) subjects() []string {
	ids := make([]string, 0, len(h))
	for id := range h {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// keys 返回标的在上期所有非零的（标的, 市场）键：冻结时按标的展开成这些键。
func (h heldIndex) keys(id string) []string {
	entries := h[id]
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, TargetKey(id, entry.market))
	}
	return keys
}

// positiveMarket 返回标的上期有正权重目标所在的市场（粘住规则）；两个市场都有时取 prefer；没有返回空串。
// 只看正权重：上期在永续上的空头不会把本期多头粘到永续上。
func (h heldIndex) positiveMarket(id, prefer string) string {
	found := ""
	for _, entry := range h[id] {
		if entry.weight.IsNegative() {
			continue
		}
		if entry.market == prefer {
			return prefer
		}
		if found == "" {
			found = entry.market
		}
	}
	return found
}

// otherMarket 返回两市场模式里 market 之外的另一个市场。
func otherMarket(mode dsl.Market, market string) string {
	for _, m := range mode.Markets() {
		if m != market {
			return m
		}
	}
	return market
}

// selectPool 做选币池构造（设计 3.1，第 1～3、5 步）：为每个标的在各市场的行里选出一行，记下本行市场与
// 两个市场的可用标记，并合并失败证据。选出的行写入 Rows，失败证据写入 FailedColumns。
//
//   - 上期持有正权重目标的标的（select: both 时），只取它持仓所在市场的行；那一行本期不存在时按无行处理，
//     走缺数和冻结逻辑，不退回去取另一个市场的行。
//   - 其余标的：select: both 时两个市场都可用只留 prefer 那一行，只在一边可用就留那一边；select 为单个市场时只用该市场的行。
func selectPool(mode dsl.Market, frame Frame, held heldIndex) Frame {
	frame.Rows = make(map[string]Row, len(frame.Universe))
	frame.RowMarket = make(map[string]string, len(frame.Universe))
	frame.FailedColumns = make(map[string]map[string]string)
	subjects := poolSubjects(frame)
	frame.avail = make(map[string]map[string]bool, len(subjects))
	markets := mode.Markets()
	addFailure := func(column, subject, reason string) {
		if frame.FailedColumns[column] == nil {
			frame.FailedColumns[column] = make(map[string]string)
		}
		if _, exists := frame.FailedColumns[column][subject]; !exists {
			frame.FailedColumns[column][subject] = reason
		}
	}
	for _, id := range subjects {
		flags := make(map[string]bool, len(markets))
		for _, market := range markets {
			_, ok := frame.Markets[market].Rows[id]
			flags[market] = ok
		}
		frame.avail[id] = flags
		for _, market := range pickOrder(mode, id, held) {
			if row, ok := frame.Markets[market].Rows[id]; ok {
				frame.Rows[id] = row
				frame.RowMarket[id] = market
				break
			}
		}
	}
	// 失败证据：有行的标的取所选行所在市场的证据；无行的标的按取行顺序合并各市场的证据，先到先得。
	for _, id := range subjects {
		order := pickOrder(mode, id, held)
		if market, ok := frame.RowMarket[id]; ok {
			order = []string{market}
		}
		for _, market := range order {
			for column, subjects := range frame.Markets[market].Failed {
				if reason, failed := subjects[id]; failed {
					addFailure(column, id, reason)
				}
			}
		}
	}
	return frame
}

// poolSubjects 返回需要选行的标的：基础集合加上各规则预期集合里的标的（排序）。
func poolSubjects(frame Frame) []string {
	set := make(map[string]struct{}, len(frame.Universe))
	for _, id := range frame.Universe {
		set[id] = struct{}{}
	}
	for _, ids := range frame.Expected {
		for _, id := range ids {
			set[id] = struct{}{}
		}
	}
	subjects := make([]string, 0, len(set))
	for id := range set {
		subjects = append(subjects, id)
	}
	sort.Strings(subjects)
	return subjects
}

// pickOrder 返回标的取行时依次尝试的市场。
func pickOrder(mode dsl.Market, id string, held heldIndex) []string {
	if mode.Select != dsl.MarketBoth {
		return []string{mode.Select}
	}
	if hold := held.positiveMarket(id, mode.Prefer); hold != "" {
		return []string{hold}
	}
	return []string{mode.Prefer, otherMarket(mode, mode.Prefer)}
}

// restrictShortCandidates 做空头候选过滤（设计 3.1 第 4 步）：空头只走永续，新开空头的候选只保留本期永续可用的标的；
// 规则上期已持有的标的不过滤，永续本期不可用时由冻结逻辑保持持仓，不能因为一期没有永续数据就把空头平掉。
// 只在同时涉及现货与永续的模式下过滤，纯永续策略的缺数照旧计入缺数。
func restrictShortCandidates(program *dsl.Program, mode dsl.Market, frame Frame, previous State) Frame {
	if mode.Single() || !mode.Has(dsl.MarketSwap) {
		return frame
	}
	expected := make(map[string][]string, len(frame.Expected))
	for ruleID, ids := range frame.Expected {
		expected[ruleID] = ids
	}
	for i := range program.Rules {
		rule := program.Rules[i].Rule
		if rule.Type == dsl.RuleTypeTranche || rule.Side != dsl.SideShort {
			continue
		}
		heldByRule := previous.Rules[rule.ID].Held
		kept := make([]string, 0, len(frame.Expected[rule.ID]))
		for _, id := range frame.Expected[rule.ID] {
			if frame.avail[id][dsl.MarketSwap] || contains(heldByRule, id) {
				kept = append(kept, id)
			}
		}
		expected[rule.ID] = kept
	}
	frame.Expected = expected
	return frame
}

// contributionMarket 给标的的一个权重定下市场（设计 3.1“定市场”）：
// 空头一律永续（纯现货与股票策略没有永续，保持原市场，由组合约束报现货负权重的错）；多头按粘住、prefer、另一个市场的顺序。
func contributionMarket(mode dsl.Market, frame Frame, held heldIndex, id string, negative bool) string {
	if mode.Single() {
		return mode.Select
	}
	if negative {
		return dsl.MarketSwap
	}
	if hold := held.positiveMarket(id, mode.Prefer); hold != "" {
		return hold
	}
	if frame.avail[id][mode.Prefer] {
		return mode.Prefer
	}
	return otherMarket(mode, mode.Prefer)
}

// labelItems 给解释明细标上市场：入选的权重取它进入的市场，其余取所选行所在的市场；单市场策略总是那个市场。
func labelItems(mode dsl.Market, frame Frame, held heldIndex, items []Item) {
	for i := range items {
		item := &items[i]
		switch {
		case mode.Single():
			item.Market = mode.Select
		case item.Stage == StageWeighted && item.Weight != "":
			item.Market = contributionMarket(mode, frame, held, item.InstrumentID, strings.HasPrefix(item.Weight, "-"))
		default:
			item.Market = frame.RowMarket[item.InstrumentID]
		}
	}
}
