package engine

import (
	"math"
	"strconv"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 止损止盈（设计第 4 节）：按 K 线收盘判断，触发后平掉该持仓单位，并在 [T, T+cooldown_bars] 内不让该标的在本规则重新入场。

// StageStopped：本期因止损止盈失去全部权重。
const StageStopped = "stopped"

// 止损止盈的明细原因；同期多项触发时按 stop_loss > trailing_stop > take_profit 取第一个。
const (
	reasonStopLoss     = "stop_loss"
	reasonTrailingStop = "trailing_stop"
	reasonTakeProfit   = "take_profit"
	reasonCooldown     = "cooldown"
)

// StopEntry 是一个持仓单位的入场价、入场 bar 序号与此前最有利的价格（字符串保存，JSON 往返无损）。
type StopEntry struct {
	Price string `json:"price"`
	Bar   int64  `json:"bar"`
	Peak  string `json:"peak"`
}

// stopPrices 是一个标的本期用于判断的价格：close 用于新入场价，adverse 判断止损，favorable 判断止盈并更新最有利价格。
type stopPrices struct {
	close, adverse, favorable float64
}

func validPrice(value float64, ok bool) bool {
	return ok && !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

// readStopPrices 读取标的本期的参考价；任一需要的价格缺失、非有限或不大于 0 时返回 false（本期不判止损、不更新最有利价格）。
func readStopPrices(row Row, hasRow bool, short bool, mode string) (stopPrices, bool) {
	if !hasRow {
		return stopPrices{}, false
	}
	closeValue, ok := row.Values["close"]
	if !validPrice(closeValue, ok) {
		return stopPrices{}, false
	}
	prices := stopPrices{close: closeValue, adverse: closeValue, favorable: closeValue}
	if mode != dsl.StopPriceExtreme {
		return prices, true
	}
	high, okHigh := row.Values["high"]
	low, okLow := row.Values["low"]
	if !validPrice(high, okHigh) || !validPrice(low, okLow) {
		return stopPrices{}, false
	}
	if short {
		prices.adverse, prices.favorable = high, low
	} else {
		prices.adverse, prices.favorable = low, high
	}
	return prices, true
}

// stopEpsilon 是阈值比较的浮点余量：100 × (1 − 0.05) 之类的乘积会差最后一位，边界上的价格应当触发。
const stopEpsilon = 1e-12

func formatPrice(value float64) string { return strconv.FormatFloat(value, 'g', -1, 64) }

func newStopEntry(price float64, bar int64) StopEntry {
	text := formatPrice(price)
	return StopEntry{Price: text, Bar: bar, Peak: text}
}

// evaluateStop 用本期价格判断一个持仓单位：跟踪止损用此前的最有利价格（不含本期），因为同一根 K 线里高低点的先后未知。
// 返回触发原因，未触发时为空。入场价或最有利价格无法解析时不触发。
func evaluateStop(stop *dsl.Stop, entry StopEntry, prices stopPrices, short bool) string {
	price, err := strconv.ParseFloat(entry.Price, 64)
	if err != nil || price <= 0 {
		return ""
	}
	peak, err := strconv.ParseFloat(entry.Peak, 64)
	if err != nil || peak <= 0 {
		peak = price
	}
	// 变动幅度按比例计算，达到阈值（含等于，留 1e-12 的浮点余量）即触发。
	lossMove, trailMove, profitMove := 1-prices.adverse/price, 1-prices.adverse/peak, prices.favorable/price-1
	if short {
		lossMove, trailMove, profitMove = prices.adverse/price-1, prices.adverse/peak-1, 1-prices.favorable/price
	}
	// 余量取阈值的一半与 stopEpsilon 的较小者，极小的阈值在价格不变时也不会触发。
	reached := func(move, threshold float64) bool {
		return threshold > 0 && move >= threshold-math.Min(stopEpsilon, threshold/2)
	}
	switch {
	case reached(lossMove, stop.Loss):
		return reasonStopLoss
	case reached(trailMove, stop.Trailing):
		return reasonTrailingStop
	case reached(profitMove, stop.Profit):
		return reasonTakeProfit
	}
	return ""
}

// stopReasonRank 是同一标的多个持仓单位同期触发时，明细原因的优先级（设计 4.3）。
var stopReasonRank = map[string]int{reasonStopLoss: 3, reasonTrailingStop: 2, reasonTakeProfit: 1}

// strongerStopReason 返回两个原因中优先级高的一个（空串最低），不随批次或开仓记录的处理顺序变化。
func strongerStopReason(current, next string) string {
	if stopReasonRank[next] > stopReasonRank[current] {
		return next
	}
	return current
}

// advancePeak 在判断之后用本期有利方向的价格更新最有利价格。
func advancePeak(entry StopEntry, prices stopPrices, short bool) StopEntry {
	peak, err := strconv.ParseFloat(entry.Peak, 64)
	if err != nil || peak <= 0 {
		peak, _ = strconv.ParseFloat(entry.Price, 64)
	}
	if (short && prices.favorable < peak) || (!short && prices.favorable > peak) {
		entry.Peak = formatPrice(prices.favorable)
	}
	return entry
}

// inCooldown 判断标的本期是否在冷却区间内（截止序号含当期）。
func inCooldown(cooldown map[string]int64, id string, bar int64) bool {
	until, ok := cooldown[id]
	return ok && bar <= until
}

// stopTracker 是 rank、signal 规则一期的止损处理：同一规则的全部持仓单位共用一组参数与方向。nil 表示规则没有止损。
type stopTracker struct {
	stop  *dsl.Stop
	short bool
	frame Frame
	// stopped 是本期触发的标的与首个原因；units 是每个标的被止损的持仓单位数（holding 的多个批次可能各触发一次）。
	stopped  map[string]string
	units    map[string]int
	counts   map[string]int
	cooldown map[string]int64
}

func newStopTracker(rule *dsl.CompiledRule, frame Frame, previous RuleState) *stopTracker {
	if rule.Rule.Stop == nil {
		return nil
	}
	cooldown := make(map[string]int64, len(previous.Cooldown))
	for id, until := range previous.Cooldown {
		if until >= frame.BarIndex {
			cooldown[id] = until
		}
	}
	return &stopTracker{stop: rule.Rule.Stop, short: rule.Rule.Side == dsl.SideShort, frame: frame,
		stopped: map[string]string{}, units: map[string]int{}, counts: map[string]int{}, cooldown: cooldown}
}

// check 判断一个持仓单位：触发时记录原因与冷却并返回 true；未触发时返回更新了最有利价格的入场记录；
// 本期没有参考价时原样返回。
func (t *stopTracker) check(id string, entry StopEntry) (StopEntry, bool) {
	row, hasRow := t.frame.Rows[id]
	prices, ok := readStopPrices(row, hasRow, t.short, t.stop.Price)
	if !ok {
		return entry, false
	}
	if reason := evaluateStop(t.stop, entry, prices, t.short); reason != "" {
		t.stopped[id] = strongerStopReason(t.stopped[id], reason)
		t.units[id]++
		t.counts[reason]++
		t.cooldown[id] = t.frame.BarIndex + int64(t.stop.CooldownBars)
		return entry, true
	}
	return advancePeak(entry, prices, t.short), false
}

// justStopped 判断标的本期是否刚止损。
func (t *stopTracker) justStopped(id string) bool {
	if t == nil {
		return false
	}
	_, ok := t.stopped[id]
	return ok
}

// excluded 判断标的本期是否不能入场：本期刚止损或在冷却中。
func (t *stopTracker) excluded(id string) bool {
	if t == nil {
		return false
	}
	if _, ok := t.stopped[id]; ok {
		return true
	}
	return inCooldown(t.cooldown, id, t.frame.BarIndex)
}

// open 返回标的本期入场的记录；本期 close 无效时返回 false（入场价下一个有价格的期次补记）。
func (t *stopTracker) open(id string) (StopEntry, bool) {
	row, hasRow := t.frame.Rows[id]
	prices, ok := readStopPrices(row, hasRow, t.short, dsl.StopPriceClose)
	if !ok {
		return StopEntry{}, false
	}
	return newStopEntry(prices.close, t.frame.BarIndex), true
}

// cooldownState 返回下一期仍有效的冷却表（截止序号晚于本期的）。
func (t *stopTracker) cooldownState() map[string]int64 {
	out := make(map[string]int64)
	for id, until := range t.cooldown {
		if until > t.frame.BarIndex {
			out[id] = until
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyStopItems 把本期止损写进解释明细与规则结果：失去全部权重的标的记 stopped（原因附上原有的淘汰原因），
// 仍有权重的在原因后追加 ;stopped:<n>。
func applyStopItems(explain *explanation, weights map[string]bool, stopped map[string]string, units map[string]int) {
	for id, reason := range stopped {
		if weights[id] {
			item := explain.items[id]
			suffix := "stopped:" + strconv.Itoa(units[id])
			if item.Reason == "" {
				item.Reason = suffix
			} else {
				item.Reason += ";" + suffix
			}
			explain.items[id] = item
			continue
		}
		item := Item{RuleID: explain.ruleID, InstrumentID: id, Stage: StageStopped, Reason: reason}
		if previous, ok := explain.items[id]; ok && previous.Reason != "" {
			item.Reason += ";" + previous.Reason
			item.Score, item.Rank = previous.Score, previous.Rank
		}
		explain.items[id] = item
	}
}
