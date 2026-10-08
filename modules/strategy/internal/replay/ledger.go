// Package replay 是基于 View 的研究回放：按周期装配历史帧、复用引擎求值，并用现货理论账本模拟成交。
package replay

import (
	"math"
	"sort"
)

const (
	convergenceTolerance = 1e-9
	maxIterations        = 10
	// dustRatio 是相对总权益的粉尘比例：不超过 dustRatio × max(权益, 1) 的金额视为浮点误差，
	// 不交易、不算目标未达成，也不保留为持仓。绝对阈值会在初始权益较大时把迭代残差误报为未成交。
	dustRatio = 1e-9
)

// Position 是一个标的的理论持仓。
type Position struct {
	Quantity    float64 `json:"quantity"`
	LastPrice   float64 `json:"last_price"`
	Value       float64 `json:"value"`
	Frozen      bool    `json:"frozen,omitempty"`
	MissingBars int     `json:"missing_bars,omitempty"`
}

// Ledger 是现货理论账本：现金与持仓数量，不读 Trade。
type Ledger struct {
	Cash           float64
	Positions      map[string]*Position
	FeeRate        float64
	LiquidateAfter int
}

// NewLedger 用初始现金构造账本；feeBps 是单边手续费（基点）。
func NewLedger(initialCash, feeBps float64, liquidateAfter int) *Ledger {
	return &Ledger{Cash: initialCash, Positions: map[string]*Position{}, FeeRate: feeBps / 10000, LiquidateAfter: liquidateAfter}
}

// Step 的输入：当期收盘价（只含有价的标的）与目标权重。OK 为 false 表示本期 skipped。
type Step struct {
	Prices  map[string]float64
	Targets map[string]float64
	OK      bool
}

// Outcome 是一期账本变化。
type Outcome struct {
	EquityBefore float64  `json:"equity_before"`
	EquityAfter  float64  `json:"equity_after"`
	Traded       float64  `json:"traded"`
	Fee          float64  `json:"fee"`
	Turnover     float64  `json:"turnover"`
	Unfilled     []string `json:"unfilled,omitempty"`
	Liquidated   []string `json:"liquidated,omitempty"`
	// BuyScale 只在现金不足、买入被按比例缩减时给出（现金为 0 时为 0）。
	BuyScale *float64 `json:"buy_scale,omitempty"`
}

// Equity 返回当前估值下的总权益（含冻结持仓）。
func (l *Ledger) Equity() float64 {
	total := l.Cash
	for _, position := range l.Positions {
		total += position.Value
	}
	return total
}

// Holdings 返回非零持仓的数量。
func (l *Ledger) Holdings() int {
	return len(l.Positions)
}

// Step 推进一期：先按当期价格估值（无价持仓冻结并累计缺价期数）；skipped 期到此为止。
// ok 期依次：清算缺价达到阈值的冻结持仓 → 按总权益迭代扣费后的目标金额 → 先卖后买（买入受现金约束，按比例缩减）。
func (l *Ledger) Step(step Step) Outcome {
	for _, id := range l.sortedIDs() {
		position := l.Positions[id]
		if price, ok := step.Prices[id]; ok && price > 0 {
			position.LastPrice = price
			position.Frozen = false
			position.MissingBars = 0
		} else {
			position.Frozen = true
			position.MissingBars++
		}
		position.Value = position.Quantity * position.LastPrice
	}
	outcome := Outcome{EquityBefore: l.Equity()}
	dust := dustRatio * math.Max(outcome.EquityBefore, 1)
	if !step.OK {
		outcome.EquityAfter = outcome.EquityBefore
		return outcome
	}
	for _, id := range l.sortedIDs() {
		position := l.Positions[id]
		if position.Frozen && l.LiquidateAfter > 0 && position.MissingBars >= l.LiquidateAfter {
			fee := position.Value * l.FeeRate
			l.Cash += position.Value - fee
			outcome.Traded += position.Value
			outcome.Fee += fee
			outcome.Liquidated = append(outcome.Liquidated, id)
			delete(l.Positions, id)
		}
	}
	equity := l.Equity()
	// 可成交集合：当期有价的持仓与目标标的。
	tradable := make(map[string]float64)
	for id, price := range step.Prices {
		if price <= 0 {
			continue
		}
		if _, held := l.Positions[id]; held {
			tradable[id] = price
			continue
		}
		if weight := step.Targets[id]; weight != 0 {
			tradable[id] = price
		}
	}
	unfilled := make(map[string]struct{})
	for id, weight := range step.Targets {
		if _, ok := tradable[id]; ok {
			continue
		}
		position, held := l.Positions[id]
		if !held {
			if weight != 0 {
				unfilled[id] = struct{}{}
			}
			continue
		}
		if math.Abs(weight*equity-position.Value) > convergenceTolerance*math.Max(equity, 1) {
			unfilled[id] = struct{}{}
		}
	}
	for id, position := range l.Positions {
		if !position.Frozen {
			continue
		}
		if _, targeted := step.Targets[id]; !targeted && position.Value > dust {
			unfilled[id] = struct{}{}
		}
	}
	value := func(id string) float64 {
		if position, ok := l.Positions[id]; ok {
			return position.Value
		}
		return 0
	}
	adjusted := equity
	for iteration := 0; iteration < maxIterations; iteration++ {
		cost := 0.0
		for id := range tradable {
			cost += math.Abs(step.Targets[id]*adjusted - value(id))
		}
		next := equity - l.FeeRate*cost
		converged := math.Abs(next-adjusted) <= convergenceTolerance*math.Max(math.Abs(equity), 1)
		adjusted = next
		if converged {
			break
		}
	}
	ids := make([]string, 0, len(tradable))
	for id := range tradable {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	desired := make(map[string]float64, len(ids))
	for _, id := range ids {
		desired[id] = step.Targets[id] * adjusted
	}
	for _, id := range ids {
		current := value(id)
		if desired[id] >= current-dust {
			continue
		}
		sell := current - desired[id]
		fee := sell * l.FeeRate
		l.Cash += sell - fee
		outcome.Traded += sell
		outcome.Fee += fee
		l.adjust(id, -sell, tradable[id], dust)
	}
	buys := make(map[string]float64)
	total := 0.0
	for _, id := range ids {
		if amount := desired[id] - value(id); amount > dust {
			buys[id] = amount
			total += amount
		}
	}
	if total > 0 {
		scale := 1.0
		need := total * (1 + l.FeeRate)
		if need > l.Cash {
			scale = math.Max(l.Cash, 0) / need
			// 迭代误差造成的粉尘级缺口只做截断，不算目标未达成。
			if need-l.Cash > dust {
				reported := scale
				outcome.BuyScale = &reported
				for id := range buys {
					unfilled[id] = struct{}{}
				}
			}
		}
		for _, id := range ids {
			amount, ok := buys[id]
			if !ok {
				continue
			}
			buy := amount * scale
			if buy <= dust {
				continue
			}
			fee := buy * l.FeeRate
			l.Cash -= buy + fee
			outcome.Traded += buy
			outcome.Fee += fee
			l.adjust(id, buy, tradable[id], dust)
		}
		if l.Cash < 0 && l.Cash > -1e-9*math.Max(equity, 1) {
			l.Cash = 0
		}
	}
	for id := range unfilled {
		outcome.Unfilled = append(outcome.Unfilled, id)
	}
	sort.Strings(outcome.Unfilled)
	outcome.EquityAfter = l.Equity()
	if outcome.EquityBefore > 0 {
		outcome.Turnover = outcome.Traded / outcome.EquityBefore
	}
	return outcome
}

// adjust 按金额增减持仓（price 为成交价）；剩余价值不超过粉尘阈值时删除持仓。
func (l *Ledger) adjust(id string, amount, price, dust float64) {
	position, ok := l.Positions[id]
	if !ok {
		position = &Position{LastPrice: price}
		l.Positions[id] = position
	}
	position.LastPrice = price
	position.Quantity += amount / price
	position.Value = position.Quantity * price
	if position.Value <= dust {
		delete(l.Positions, id)
	}
}

func (l *Ledger) sortedIDs() []string {
	ids := make([]string, 0, len(l.Positions))
	for id := range l.Positions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Snapshot 返回持仓与现金的拷贝（写入 positions_json）。
func (l *Ledger) Snapshot() map[string]any {
	positions := make(map[string]Position, len(l.Positions))
	for id, position := range l.Positions {
		positions[id] = *position
	}
	return map[string]any{"cash": l.Cash, "positions": positions}
}
