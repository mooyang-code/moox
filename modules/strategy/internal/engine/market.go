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

// HeldMarkets 返回上期各市场持有非零目标（多空都算）的标的（市场 → 标的）。
func (s State) HeldMarkets() map[string]map[string]struct{} {
	held := make(map[string]map[string]struct{})
	for id, entries := range indexHeld(s) {
		for _, entry := range entries {
			if held[entry.market] == nil {
				held[entry.market] = make(map[string]struct{})
			}
			held[entry.market][id] = struct{}{}
		}
	}
	return held
}

// holds 报告上期在这个市场持有标的的非零目标。
func (h heldIndex) holds(id, market string) bool {
	for _, entry := range h[id] {
		if entry.market == market {
			return true
		}
	}
	return false
}

// shortIn 报告上期在这个市场持有标的的空头（负权重目标）。
func (h heldIndex) shortIn(id, market string) bool {
	for _, entry := range h[id] {
		if entry.market == market && entry.weight.IsNegative() {
			return true
		}
	}
	return false
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
//   - 上期持有正权重目标的标的（所有模式），只取它持仓所在市场的行；那一行本期不存在时按无行处理，
//     走缺数和冻结逻辑，不退回去取另一个市场的行。
//   - 其余标的：select: both 时两个市场都可用只留 prefer 那一行，只在一边可用就留那一边；select 为单个市场时只用该市场的行。
//
// columns 是策略引用的全部列（当期列与 bars[-1] 列），用来判断失败证据是否覆盖了全部引用列。
func selectPool(mode dsl.Market, frame Frame, held heldIndex, columns []string) Frame {
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
	// usableRow 返回标的在一个市场本期可用的行：这个市场里年龄不够的标的不用它的行，持仓豁免（见 MarketInput.AgedOut）。
	usableRow := func(market, id string) (Row, bool) {
		input := frame.Markets[market]
		row, ok := input.Rows[id]
		if !ok {
			return Row{}, false
		}
		if _, aged := input.AgedOut[id]; aged && !held.holds(id, market) {
			return Row{}, false
		}
		return row, true
	}
	// 上期在某个市场持有空头、而这个市场本期对它的失败证据全都可冻结时，只取这个市场的行（有行取行，没行按无行），证据随之归到这个市场，
	// 由冻结逻辑保持，不退回另一个市场的行；否则空头（取行顺序是 prefer 加另一个市场，粘住只看正权重）会改用现货的因子值重新排名，
	// 在永续上平仓或改仓。条件两条都要满足：① 这个市场对这个币至少有一条证据，并且每条都是整体不可用（FailedMarket）；
	// 混有 factor_failed 等逐币原因时单市场下也不冻结，照常按 pickOrder 取行。
	// ② 这个市场本期有它的行（失败序列写成 NULL 行），或者证据覆盖策略引用的全部列（没有行、只有部分列有证据时，其余列是无证据的缺失，
	// 单市场下同样不冻结，也按 pickOrder）。冻结按币：同一个币持有现货多头又持有永续空头时，满足条件就把现货多头一起冻结（保守，已知边界）。
	freezable := func(market, id string) bool {
		covered := make(map[string]struct{})
		for column, subjects := range frame.Markets[market].Failed {
			reason, failed := subjects[id]
			if !failed {
				continue
			}
			if reason != FailedMarket {
				return false
			}
			covered[column] = struct{}{}
		}
		if len(covered) == 0 {
			return false
		}
		if _, ok := usableRow(market, id); ok {
			return true
		}
		for _, column := range columns {
			if _, ok := covered[column]; !ok {
				return false
			}
		}
		return true
	}
	orderOf := func(id string) []string {
		for _, entry := range held[id] {
			if entry.weight.IsNegative() && mode.Has(entry.market) && freezable(entry.market, id) {
				return []string{entry.market}
			}
		}
		return pickOrder(mode, id, held)
	}
	for _, id := range subjects {
		flags := make(map[string]bool, len(markets))
		for _, market := range markets {
			_, ok := usableRow(market, id)
			flags[market] = ok
		}
		frame.avail[id] = flags
		for _, market := range orderOf(id) {
			if row, ok := usableRow(market, id); ok {
				frame.Rows[id] = row
				frame.RowMarket[id] = market
				break
			}
		}
	}
	// 失败证据：有行的标的取所选行所在市场的证据；无行的标的按取行顺序合并各市场的证据，先到先得。
	for _, id := range subjects {
		order := orderOf(id)
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
	// 所有模式都先取持仓所在市场的行（设计 3.1 第 2 步）：冻结保护和止损用的始终是持仓市场的数据。
	if hold := held.positiveMarket(id, mode.Prefer); hold != "" && mode.Has(hold) {
		return []string{hold}
	}
	if mode.Select != dsl.MarketBoth {
		return []string{mode.Select}
	}
	return []string{mode.Prefer, otherMarket(mode, mode.Prefer)}
}

// filtersShortCandidates 报告这条规则是否做空头候选过滤：同时涉及现货与永续的模式下、非 tranche 的空头规则。
func filtersShortCandidates(mode dsl.Market, rule dsl.Rule) bool {
	return !mode.Single() && mode.Has(dsl.MarketSwap) && rule.Type != dsl.RuleTypeTranche && rule.Side == dsl.SideShort
}

// marketUnavailable 报告这个市场本期整体不可用：帧里它的失败证据有 market_unavailable（input 对整个市场的每个标的都记这个原因）。
func marketUnavailable(frame Frame, market string) bool {
	for _, subjects := range frame.Markets[market].Failed {
		for _, reason := range subjects {
			if reason == FailedMarket {
				return true
			}
		}
	}
	return false
}

// waivesShortGuard 报告这条规则本期不做守门：多市场模式下永续本期整体不可用时的空头规则（设计 3.2.1：只有一部分市场不可用时不整期跳过），
// 并且规则本期的可用集合与缺数都为空。空头只走永续，永续整体不可用时空头候选过滤之后它的预期集合只剩上期持有的空头，证据齐全时全部按市场不可用冻结，
// 守门必然判 universe_too_small 或 too_many_missing，把多头腿也一起整期跳过；冻结的空头由 holdTargets 单独产出保持目标，规则本身没有可用候选、不配权。
// 可用或缺数不为空说明有持有的空头没被冻结（例如证据没覆盖到、借用了现货行），这时不放开、照常守门：缺数比例超过 max_missing 才整期跳过，
// 没超过时它仍会在现货行上重新排名。真正的保护在输入层：不可用市场的证据覆盖上期在这个市场的全部持仓（input.marketUnavailable）。
func waivesShortGuard(mode dsl.Market, rule dsl.Rule, frame Frame, sets ruleSets) bool {
	return filtersShortCandidates(mode, rule) && marketUnavailable(frame, dsl.MarketSwap) && len(sets.available) == 0 && len(sets.missing) == 0
}

// restrictShortCandidates 做空头候选过滤（设计 3.1 第 4 步）：空头只走永续，新开空头的候选只保留本期永续可用的标的；
// 规则上期已持有的标的不过滤，永续本期不可用时由冻结逻辑保持持仓，不能因为一期没有永续数据就把空头平掉。
// 只在同时涉及现货与永续的模式下过滤，纯永续策略的缺数照旧计入缺数。
//
// 同时记下空头规则里按无行处理的持仓（Frame.shortNoRow）：规则上期持有、上期在永续上是空头，本期永续没有它的行、也没有任何失败证据，
// 并且永续不是整体不可用（例如这个币的永续已下架、现货还在）。这样的空头不再借用现货行继续排名（空头只能下在永续，永续上已经没有它），
// 在这条规则里记 no_row 缺数并平掉，与上期持有的永续多头在永续无行时被平掉对称；按规则处理，同一个币的现货多头和其他规则照常用现货行。
// 永续有证据时由选币池构造与冻结逻辑处理，永续整体不可用时由冻结逻辑保持（证据覆盖不全时守门兜底，见 waivesShortGuard）。
func restrictShortCandidates(program *dsl.Program, mode dsl.Market, frame Frame, previous State, held heldIndex) Frame {
	if mode.Single() || !mode.Has(dsl.MarketSwap) {
		return frame
	}
	expected := make(map[string][]string, len(frame.Expected))
	for ruleID, ids := range frame.Expected {
		expected[ruleID] = ids
	}
	agedOut := make(map[string][]string, len(frame.AgedOut))
	for ruleID, ids := range frame.AgedOut {
		agedOut[ruleID] = ids
	}
	swapDown := marketUnavailable(frame, dsl.MarketSwap)
	swapEvidence := func(id string) bool {
		for _, subjects := range frame.Markets[dsl.MarketSwap].Failed {
			if _, failed := subjects[id]; failed {
				return true
			}
		}
		return false
	}
	for i := range program.Rules {
		rule := program.Rules[i].Rule
		if !filtersShortCandidates(mode, rule) {
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
		if !swapDown {
			for _, id := range heldByRule {
				if frame.avail[id][dsl.MarketSwap] || !held.shortIn(id, dsl.MarketSwap) || swapEvidence(id) {
					continue
				}
				if frame.shortNoRow == nil {
					frame.shortNoRow = make(map[string]map[string]struct{})
				}
				if frame.shortNoRow[rule.ID] == nil {
					frame.shortNoRow[rule.ID] = make(map[string]struct{})
				}
				frame.shortNoRow[rule.ID][id] = struct{}{}
			}
		}
		// 年龄剔除只统计仍在预期集合里的标的：被空头候选过滤掉的标的不再算进“预期 − 年龄剔除”的分母。
		inExpected := make(map[string]struct{}, len(kept))
		for _, id := range kept {
			inExpected[id] = struct{}{}
		}
		aged := make([]string, 0, len(frame.AgedOut[rule.ID]))
		for _, id := range frame.AgedOut[rule.ID] {
			if _, ok := inExpected[id]; ok {
				aged = append(aged, id)
			}
		}
		agedOut[rule.ID] = aged
	}
	frame.Expected, frame.AgedOut = expected, agedOut
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
// 冻结（保持持仓）的标的取上期持仓所在的市场（有行也一样，行可能来自另一个市场）：空头规则取它的空头所在市场（永续），
// 其余规则取正权重持仓的市场；没有对应方向的持仓时退回任一持仓市场，都没有才取所选行的市场。
func labelItems(program *dsl.Program, mode dsl.Market, frame Frame, held heldIndex, items []Item) {
	shortRules := make(map[string]bool, len(program.Rules))
	for i := range program.Rules {
		shortRules[program.Rules[i].Rule.ID] = program.Rules[i].Rule.Side == dsl.SideShort
	}
	for i := range items {
		item := &items[i]
		switch {
		case mode.Single():
			item.Market = mode.Select
		case item.Stage == StageWeighted && item.Weight != "":
			item.Market = contributionMarket(mode, frame, held, item.InstrumentID, strings.HasPrefix(item.Weight, "-"))
		case item.Stage == StageHeld:
			// 冻结的明细按规则方向标上期持仓所在的市场：所选行可能来自另一个市场（例如对冲币多头规则取的是永续的行）。
			item.Market = held.heldMarket(item.InstrumentID, shortRules[item.RuleID], mode.Prefer)
			if item.Market == "" {
				item.Market = frame.RowMarket[item.InstrumentID]
			}
		default:
			item.Market = frame.RowMarket[item.InstrumentID]
			if _, noRow := frame.shortNoRow[item.RuleID][item.InstrumentID]; noRow {
				item.Market = dsl.MarketSwap // 按无行处理的永续空头：缺的是永续的行
			}
		}
	}
}

// heldMarket 返回标的上期持仓所在的市场：short 为 true 时取负权重目标的市场，否则取正权重目标的市场（两个市场都有时取 prefer）；
// 没有对应方向的持仓时退回任一持仓市场，都没有返回空串。
func (h heldIndex) heldMarket(id string, short bool, prefer string) string {
	if !short {
		if market := h.positiveMarket(id, prefer); market != "" {
			return market
		}
	}
	for _, entry := range h[id] {
		if entry.weight.IsNegative() {
			return entry.market
		}
	}
	if entries := h[id]; len(entries) > 0 {
		return entries[0].market
	}
	return ""
}
