// Package pool 是策略池的求值核心（设计 3.5）：每期各成员先独立求出目标，池按成员模拟账本的净值分配资金，再合成池目标。
// 它是纯函数：实时求值、预热与测试里的历史推进都调用同一个 Step，状态只通过 engine.State 传递。
package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/ledger"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

const (
	// InitialEquity 是成员模拟账本的初始权益：净值序列从 1 起算，只用相对值。
	InitialEquity = 1
	// LiquidateBars 是成员账本里缺价的持仓连续缺价这么多期后清算（与研究回放的默认值一致）。
	LiquidateBars = 3
)

// Config 是池的静态配置，由池的 DSL 与成员市场的并集构成。
type Config struct {
	Spec      dsl.StrategyPool
	Portfolio dsl.Portfolio
	// Mode 是池的有效市场模式（成员市场的并集，见 dsl.UnionMarket）。
	Mode dsl.Market
	// Degrading 非空时报告一个跳过原因是否需要人工处理：所有成员都没通过时，池的跳过原因优先取这类原因（实例据此标 degraded）。
	Degrading func(reason string) bool
}

// PickSkipReason 在所有成员都没通过时选池的跳过原因：需要人工处理的原因优先，否则取第一个成员的原因。
// 就绪判断阶段与求值阶段用同一个口径。reasonOf 返回成员没通过的原因；没有任何原因时返回 ok 为 false。
func (c Config) PickSkipReason(reasonOf func(id string) string) (id, reason string, ok bool) {
	for _, member := range c.Spec.Members {
		current := reasonOf(member.ID)
		if current == "" {
			continue
		}
		if !ok {
			id, reason, ok = member.ID, current, true
		}
		if c.Degrading != nil && c.Degrading(current) {
			return member.ID, current, true
		}
	}
	return id, reason, ok
}

// NewConfig 从池的策略定义构造配置；mode 是成员市场的并集。
func NewConfig(strategy dsl.Strategy, mode dsl.Market) (Config, error) {
	if !strategy.IsPool() {
		return Config{}, errors.New("不是策略池")
	}
	return Config{Spec: *strategy.Pool, Portfolio: strategy.Portfolio, Mode: mode}, nil
}

// MemberIDs 返回成员 id（按池里的顺序）。
func (c Config) MemberIDs() []string { return c.Spec.MemberIDs() }

// State 是池的跨期状态，序列化在 engine.State.Pool 里。
type State struct {
	// Started 为真表示已经推进过至少一期；LastBarIndex 是最后推进的那一期的 bar 序号：
	// 之后某一期缺了（没有被处理）时，成员净值沿用上一个值补齐。
	Started      bool  `json:"started,omitempty"`
	LastBarIndex int64 `json:"last_bar_index,omitempty"`
	// Ratios 是上期（步长限制之后）各成员的比例，供步长限制用。
	Ratios map[string]string `json:"ratios,omitempty"`
	// Series 是各成员模拟账本的净值序列，最后一个值是最后推进那一期的期末净值，只保留最长窗口所需的长度。
	Series map[string][]float64 `json:"series,omitempty"`
	// Ledgers 是各成员的模拟账本（全精度）。
	Ledgers map[string]*ledger.Ledger `json:"ledgers,omitempty"`
}

// DecodeState 解析 engine.State.Pool；空值是尚未开始的池。
func DecodeState(raw json.RawMessage) (State, error) {
	var state State
	if len(raw) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("池状态无法解析：%w", err)
	}
	return state, nil
}

// MemberResult 是一个成员本期的求值结果。Decision 为空或不是 ok 表示本期没有通过（沿用上一期的目标权重），
// Reason 与 Detail 说明原因（求值被跳过时取决策的跳过原因，就绪判断没过时取就绪判断的结论）。
type MemberResult struct {
	ID       string
	Decision *engine.Decision
	Reason   string
	Detail   string
}

func (r MemberResult) passed() bool { return r.Decision != nil && r.Decision.Status == engine.StatusOK }

// Input 是池一期的输入。
type Input struct {
	// BarIndex 是这根 bar 在日历中的序号。
	BarIndex int64
	// Prices 是本期各（标的|市场）的收盘价（只含有价的），键是 engine.TargetKey；成员账本的估值与成交都用它，
	// 要覆盖账本里全部持仓的键，而不只是成员本期预期的标的。
	Prices map[string]float64
	// Members 是各成员本期的结果，覆盖池的全部成员。
	Members []MemberResult
}

// Step 推进池一期。previous 是上一期的完整状态（没有时为零值）；返回的决策里 State 是新的完整状态。
// 只有所有成员都没通过时才返回 skipped：状态不变，账本不推进（缺的期之后沿用上一个净值补齐）。
// 计算顺序：成员目标（没通过的沿用上期权重）→ 用上一期末为止的净值序列算比例 → 成员账本推进一期并追加本期净值 → 池目标 = Σ 比例 × 成员目标，
// 再套组合约束与 max_weight。
func Step(cfg Config, previous engine.State, in Input) (engine.Decision, error) {
	ids := cfg.MemberIDs()
	results, err := indexResults(ids, in.Members)
	if err != nil {
		return engine.Decision{}, err
	}
	state, err := DecodeState(previous.Pool)
	if err != nil {
		return engine.Decision{}, err
	}
	items := memberItems(ids, results)
	anyPassed := false
	for _, result := range results {
		anyPassed = anyPassed || result.passed()
	}
	if !anyPassed {
		reason, detail := skipReason(cfg, ids, results)
		decision := engine.Decision{Status: engine.StatusSkipped, SkipReason: reason, State: previous, Items: items,
			Summary: engine.Summary{Rules: map[string]engine.RuleSummary{}, Notes: []string{detail}, Pool: summary(cfg, ids, results, state, nil, nil)}}
		return decision, nil
	}
	if state.Started && in.BarIndex <= state.LastBarIndex {
		return engine.Decision{}, fmt.Errorf("bar 序号 %d 没有晚于池已推进到的 %d", in.BarIndex, state.LastBarIndex)
	}
	keep := max(cfg.Spec.MaxWindow()+1, 1)
	fillGap(&state, ids, in.BarIndex)
	previousRatios, err := parseRatios(state.Ratios)
	if err != nil {
		return engine.Decision{}, err
	}
	bankrupt := make(map[string]bool, len(ids))
	for _, id := range ids {
		if book := state.Ledgers[id]; book != nil {
			bankrupt[id] = book.Bankrupt
		}
	}
	// 比例只用上一期末为止的净值序列：本期的成员账本在下面才推进。
	alloc := allocate(cfg.Spec, ids, state.Series, bankrupt, previousRatios)

	contributions := make(map[string]quant.Decimal)
	frozen := make(map[string]struct{})
	perMember := make(map[string]map[string]quant.Decimal, len(ids))
	for _, id := range ids {
		result := results[id]
		weights, err := memberWeights(result, previous.Members[id])
		if err != nil {
			return engine.Decision{}, fmt.Errorf("成员 %s：%w", id, err)
		}
		scaled := make(map[string]quant.Decimal, len(weights))
		for key, weight := range weights {
			share := weight.Mul(alloc.ratio[id])
			scaled[key] = share
			contributions[key] = contributions[key].Add(share)
		}
		perMember[id] = scaled
		if result.passed() {
			for _, subject := range engine.HeldSubjects(result.Decision.Targets) {
				frozen[subject] = struct{}{}
			}
		}
	}
	if state.Ledgers == nil {
		state.Ledgers = make(map[string]*ledger.Ledger, len(ids))
	}
	if state.Series == nil {
		state.Series = make(map[string][]float64, len(ids))
	}
	for _, id := range ids {
		book := state.Ledgers[id]
		if book == nil {
			book = ledger.NewLedger(InitialEquity, cfg.Spec.Fees.SpotBps, cfg.Spec.Fees.SwapBps, LiquidateBars)
			state.Ledgers[id] = book
		}
		outcome := book.Step(ledger.Step{Prices: in.Prices, Targets: floatTargets(results[id]), OK: results[id].passed()})
		series := append(state.Series[id], outcome.EquityAfter)
		if len(series) > keep {
			series = append([]float64(nil), series[len(series)-keep:]...)
		}
		state.Series[id] = series
	}
	composed, err := engine.ComposeTargets(cfg.Portfolio, cfg.Mode, contributions, frozen, previous)
	if err != nil {
		return engine.Decision{}, err
	}
	state.Started, state.LastBarIndex = true, in.BarIndex
	state.Ratios = make(map[string]string, len(ids))
	for _, id := range ids {
		state.Ratios[id] = alloc.ratio[id].String()
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return engine.Decision{}, err
	}
	next := engine.State{Targets: composed.State.Targets, Members: make(map[string]engine.State, len(ids)), Pool: raw}
	for _, id := range ids {
		if results[id].passed() {
			next.Members[id] = results[id].Decision.State
		} else {
			next.Members[id] = previous.Members[id]
		}
	}
	composed.State = next
	composed.Items = items
	composed.Summary.Pool = summary(cfg, ids, results, state, &alloc, perMember)
	return composed, nil
}

// indexResults 按成员 id 索引本期结果：必须恰好覆盖池的全部成员。
func indexResults(ids []string, members []MemberResult) (map[string]MemberResult, error) {
	results := make(map[string]MemberResult, len(members))
	for _, member := range members {
		if _, dup := results[member.ID]; dup {
			return nil, fmt.Errorf("成员 %s 的结果重复", member.ID)
		}
		results[member.ID] = member
	}
	for _, id := range ids {
		if _, ok := results[id]; !ok {
			return nil, fmt.Errorf("缺少成员 %s 的结果", id)
		}
	}
	if len(results) != len(ids) {
		return nil, errors.New("结果里有不属于池的成员")
	}
	return results, nil
}

// fillGap 在某一期没有被处理（跳过、事件缺失）时把成员净值沿用上一个值补齐，使序列的每个位置对应日历上连续的一期。
func fillGap(state *State, ids []string, barIndex int64) {
	if !state.Started || barIndex <= state.LastBarIndex+1 {
		return
	}
	gap := barIndex - state.LastBarIndex - 1
	for _, id := range ids {
		series := state.Series[id]
		if len(series) == 0 {
			continue
		}
		last := series[len(series)-1]
		for i := int64(0); i < gap && i < maxFill; i++ {
			series = append(series, last)
		}
		state.Series[id] = series
	}
}

// maxFill 限制一次补齐的期数：序列只保留最长窗口所需的长度，补得再多也只剩最近的部分。
const maxFill = 4096

func parseRatios(raw map[string]string) (map[string]quant.Decimal, error) {
	if raw == nil {
		return nil, nil
	}
	ratios := make(map[string]quant.Decimal, len(raw))
	for id, text := range raw {
		ratio, err := quant.Parse(text)
		if err != nil {
			return nil, fmt.Errorf("成员 %s 的上期比例 %q 无法解析：%w", id, text, err)
		}
		ratios[id] = ratio
	}
	return ratios, nil
}

// memberWeights 返回成员本期的目标权重（键是标的|市场）：通过的取本期决策的目标（保持目标取预留的权重），没通过的沿用成员上一期的目标。
func memberWeights(result MemberResult, previous engine.State) (map[string]quant.Decimal, error) {
	weights := make(map[string]quant.Decimal)
	if result.passed() {
		for _, target := range result.Decision.Targets {
			weights[engine.TargetKey(target.InstrumentID, target.Market)] = target.Weight
		}
		return weights, nil
	}
	for key, text := range previous.Targets {
		weight, err := quant.Parse(text)
		if err != nil {
			return nil, fmt.Errorf("上期目标 %s=%q 无法解析：%w", key, text, err)
		}
		if !weight.IsZero() {
			weights[key] = weight
		}
	}
	return weights, nil
}

// floatTargets 是成员账本本期的目标权重（浮点，与研究回放一致）；没通过的成员账本只估值、不调仓，目标不用。
func floatTargets(result MemberResult) map[string]float64 {
	targets := make(map[string]float64)
	if !result.passed() {
		return targets
	}
	for _, target := range result.Decision.Targets {
		weight, _ := strconv.ParseFloat(target.Weight.String(), 64)
		targets[engine.TargetKey(target.InstrumentID, target.Market)] = weight
	}
	return targets
}

// memberItems 汇总成员的求值明细：规则 id 加成员前缀（成员 id/规则 id），不同成员的规则不会撞名。
func memberItems(ids []string, results map[string]MemberResult) []engine.Item {
	var items []engine.Item
	for _, id := range ids {
		if results[id].Decision == nil {
			continue
		}
		for _, item := range results[id].Decision.Items {
			item.RuleID = id + "/" + item.RuleID
			items = append(items, item)
		}
	}
	return items
}

// skipReason 是所有成员都没通过时池的跳过原因（口径见 Config.PickSkipReason），说明里列出每个成员的原因。
func skipReason(cfg Config, ids []string, results map[string]MemberResult) (reason, detail string) {
	reason = engine.SkipNoData
	if _, picked, ok := cfg.PickSkipReason(func(id string) string { return results[id].reason() }); ok {
		reason = picked
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("成员 %s：%s%s", id, results[id].reason(), detailSuffix(results[id].detail())))
	}
	return reason, "所有成员本期都没通过，" + strings.Join(parts, "；")
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return "（" + detail + "）"
}

func (r MemberResult) reason() string {
	if r.Reason != "" {
		return r.Reason
	}
	if r.Decision != nil {
		return r.Decision.SkipReason
	}
	return ""
}

func (r MemberResult) detail() string {
	if r.Detail != "" {
		return r.Detail
	}
	if r.Decision != nil {
		return strings.Join(r.Decision.Summary.Notes, "；")
	}
	return ""
}

// summary 构造本期的池明细。alloc 与 perMember 为空表示池本期被跳过（账本没有推进，比例沿用上期）。
func summary(cfg Config, ids []string, results map[string]MemberResult, state State, alloc *allocation, perMember map[string]map[string]quant.Decimal) *engine.PoolSummary {
	out := &engine.PoolSummary{Method: cfg.Spec.Allocation.Method, Timing: cfg.Spec.Timing != nil, Members: make([]engine.PoolMemberSummary, 0, len(ids))}
	for _, id := range ids {
		result := results[id]
		member := engine.PoolMemberSummary{ID: id, Status: engine.StatusOK}
		if !result.passed() {
			member.Status, member.Reason, member.Detail = engine.StatusSkipped, result.reason(), result.detail()
		}
		if series := state.Series[id]; len(series) > 0 {
			member.Equity = series[len(series)-1]
		} else {
			member.Equity = InitialEquity
		}
		if book := state.Ledgers[id]; book != nil {
			member.Bankrupt = book.Bankrupt
		}
		ratio := state.Ratios[id]
		if ratio == "" {
			ratio = "0"
		}
		member.Base, member.RatioBefore, member.Ratio = ratio, ratio, ratio
		member.Gross, member.Net = "0", "0"
		if alloc != nil {
			member.Base, member.RatioBefore, member.Ratio = alloc.base[id].String(), alloc.before[id].String(), alloc.ratio[id].String()
			if alloc.timing != nil {
				member.Timing = alloc.timing[id].String()
			}
			if rank, ok := alloc.rank[id]; ok {
				rank = math.Round(rank*1e6) / 1e6
				member.Rank = &rank
			}
			gross, net := quant.Zero(), quant.Zero()
			member.Targets = make(map[string]string, len(perMember[id]))
			keys := make([]string, 0, len(perMember[id]))
			for key := range perMember[id] {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				share := perMember[id][key]
				if share.IsZero() {
					continue
				}
				member.Targets[key] = share.String()
				net = net.Add(share)
				if share.IsNegative() {
					share = share.Neg()
				}
				gross = gross.Add(share)
			}
			member.Gross, member.Net = gross.String(), net.String()
		}
		out.Members = append(out.Members, member)
	}
	return out
}

// PriceKeys 返回本期成员账本需要价格的（标的|市场）键：各账本里已有的持仓，加上通过的成员本期的目标。
// 调用方按这些键读取收盘价放进 Input.Prices。
func PriceKeys(previous engine.State, members []MemberResult) ([]string, error) {
	state, err := DecodeState(previous.Pool)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, book := range state.Ledgers {
		for key := range book.Positions {
			seen[key] = struct{}{}
		}
	}
	for _, member := range members {
		if !member.passed() {
			continue
		}
		for _, target := range member.Decision.Targets {
			seen[engine.TargetKey(target.InstrumentID, target.Market)] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

// SkippedSummary 构造池本期被跳过时的摘要（成员状态与原因、账本与比例沿用上期）：就绪判断阶段所有成员都没通过、不经过 Step 时用它，
// 池的详情里这些期才不是空的。results 覆盖池的全部成员。
func SkippedSummary(cfg Config, previous engine.State, results []MemberResult) (*engine.PoolSummary, error) {
	ids := cfg.MemberIDs()
	indexed, err := indexResults(ids, results)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(previous.Pool)
	if err != nil {
		return nil, err
	}
	return summary(cfg, ids, indexed, state, nil, nil), nil
}
