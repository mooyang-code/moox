// Package ledger 是理论账本：按收盘价模拟现货与永续的成交、手续费与逐期结算，研究回放与策略池的成员净值共用。
package ledger

import (
	"errors"
	"math"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
)

const (
	convergenceTolerance = 1e-9
	maxIterations        = 10
	// dustRatio 是相对总权益的粉尘比例：不超过 dustRatio × max(权益, 1) 的金额视为浮点误差，
	// 不交易、不算目标未达成，也不保留为持仓。绝对阈值会在初始权益较大时把迭代残差误报为未成交。
	dustRatio = 1e-9
)

// Position 是一个标的在一个市场的理论持仓。
type Position struct {
	// Quantity 现货不为负；永续带符号，空头为负。
	Quantity  float64 `json:"quantity"`
	LastPrice float64 `json:"last_price"`
	// Value 是 Quantity × LastPrice：现货是市值，永续是名义价值（空头为负）。
	Value       float64 `json:"value"`
	Frozen      bool    `json:"frozen,omitempty"`
	MissingBars int     `json:"missing_bars,omitempty"`
}

// Ledger 是理论账本：现金与持仓数量，不读 Trade。持仓键是 engine.TargetKey（标的 + 市场），同一个币的现货与永续是两个持仓，
// 不互相抵消；没有市场后缀的键按现货类处理。
//
// 现货按现金成交，权益里计入市值。永续不占用现金，按收盘价逐期结算：每期有价时把 Quantity × (收盘价 − 上期价) 记入现金，
// 权益 = 现金 + 现货市值，成交只改数量并扣手续费。这样永续的盈利可以用来买现货，亏损会减少现金（现金可以为负，此时现货买入被缩减为 0，
// 目标权重按缩小后的权益折算，现货随后被卖出补回现金）。没有价格的永续持仓冻结，不结算。不模拟强平与保证金率。
//
// 资金费（只在 Step.Funding 非 nil 时计）：第 k 根的费率是这一根开盘时刻的结算，那一刻持有的是第 k−1 根收盘调仓之前的仓位，
// 所以每根估值后把永续持仓的带符号名义价值存成 FundingBasis，下一根按它乘本根费率结算进现金（费率为正时多头付、空头收）。
//
// 每根依次调用 Mark（估值与结算）和 Rebalance（清算与调仓）；Step 是两者的合写。
type Ledger struct {
	Cash      float64              `json:"cash"`
	Positions map[string]*Position `json:"positions"`
	// SpotFeeRate 是现货类（现货、股票）的单边手续费率，SwapFeeRate 是永续的。
	SpotFeeRate    float64 `json:"spot_fee_rate"`
	SwapFeeRate    float64 `json:"swap_fee_rate"`
	LiquidateAfter int     `json:"liquidate_after"`
	// Bankrupt 在持仓期间权益降到不超过 0 时置位（模拟爆仓）：之后各期忽略目标，可成交的持仓全部平掉、不再开仓，空仓直到结束。
	// 没有这条规则时负权益会让“目标权重 × 权益”变成反向持仓，账户靠反向持仓“复活”。
	Bankrupt bool `json:"bankrupt,omitempty"`
	// FundingBasis 是上一根估值后、调仓前各永续持仓的带符号名义价值（按上一根收盘价），本根据此结算资金费；只在计资金费时记录。
	FundingBasis map[string]float64 `json:"funding_basis,omitempty"`
	// marked 为真表示本根已经估值、还没有调仓：同一根不能重复估值。
	marked bool
}

// NewLedger 用初始现金构造账本；spotFeeBps 与 swapFeeBps 是现货类与永续的单边手续费（基点）。
func NewLedger(initialCash, spotFeeBps, swapFeeBps float64, liquidateAfter int) *Ledger {
	return &Ledger{Cash: initialCash, Positions: map[string]*Position{}, SpotFeeRate: spotFeeBps / 10000, SwapFeeRate: swapFeeBps / 10000, LiquidateAfter: liquidateAfter}
}

// Step 的输入：当期各（标的, 市场）的收盘价（只含有价的）与目标权重，键是 engine.TargetKey。OK 为 false 表示本期 skipped。
// Funding 是本根累计的资金费率（键是永续的 engine.TargetKey，只含有值的）；nil 表示不计资金费，空映射表示计资金费但本根没有费率。
type Step struct {
	Prices  map[string]float64
	Targets map[string]float64
	OK      bool
	Funding map[string]float64
}

// Outcome 是一期账本变化。
type Outcome struct {
	EquityBefore float64 `json:"equity_before"`
	EquityAfter  float64 `json:"equity_after"`
	Traded       float64 `json:"traded"`
	Fee          float64 `json:"fee"`
	Turnover     float64 `json:"turnover"`
	// Funding 是本根结算的资金费（正数为支出、负数为收入），已经计入 EquityBefore。
	Funding    float64  `json:"funding,omitempty"`
	Unfilled   []string `json:"unfilled,omitempty"`
	Liquidated []string `json:"liquidated,omitempty"`
	// BuyScale 只在现金不足、现货买入被按比例缩减时给出（现金为 0 时为 0）。
	BuyScale *float64 `json:"buy_scale,omitempty"`
	// Bankrupt 只在权益首次降到不超过 0 的那一期为真（模拟爆仓）；此后各期空仓。
	Bankrupt bool `json:"bankrupt,omitempty"`
	// SpotValue 是期末现货类持仓的市值之和；SwapValue 是永续持仓的净名义价值（多头为正，空头为负）。
	SpotValue float64 `json:"spot_value"`
	SwapValue float64 `json:"swap_value"`
}

func isSwapKey(key string) bool {
	_, market := engine.SplitTargetKey(key)
	return market == dsl.MarketSwap
}

func (l *Ledger) feeRate(key string) float64 {
	if isSwapKey(key) {
		return l.SwapFeeRate
	}
	return l.SpotFeeRate
}

// Equity 返回当前估值下的总权益（含冻结持仓）：现金加现货市值，永续的盈亏已经结算进现金。
func (l *Ledger) Equity() float64 {
	total := l.Cash
	for _, key := range l.sortedIDs() {
		if !isSwapKey(key) {
			total += l.Positions[key].Value
		}
	}
	return total
}

// Holdings 返回非零持仓的数量。
func (l *Ledger) Holdings() int {
	return len(l.Positions)
}

// values 返回期末现货类持仓的市值之和与永续持仓的净名义价值。
func (l *Ledger) values() (spot, swap float64) {
	for _, key := range l.sortedIDs() {
		if isSwapKey(key) {
			swap += l.Positions[key].Value
		} else {
			spot += l.Positions[key].Value
		}
	}
	return spot, swap
}

// Step 推进一期，等于依次调用 Mark 与 Rebalance（不能与它们混用）。
func (l *Ledger) Step(step Step) Outcome {
	return l.rebalance(l.mark(step.Prices, step.Funding), step)
}

// Mark 是一期的估值：永续按价差结算进现金，无价持仓冻结并累计缺价期数，按上一根的快照结算资金费，判定爆仓。
// 同一根重复调用报错；之后必须调用 Rebalance（skipped 期也要调用，只做收尾）。
func (l *Ledger) Mark(prices, funding map[string]float64) (Outcome, error) {
	if l.marked {
		return Outcome{}, errors.New("账本本根已经估值，同一根不能重复调用 Mark")
	}
	outcome := l.mark(prices, funding)
	l.marked = true
	return outcome, nil
}

// Rebalance 是一期的调仓，outcome 是本根 Mark 的结果。ok 期依次：清算缺价达到阈值的冻结持仓 → 按总权益迭代扣费后的目标金额 →
// 先卖现货、再调永续、最后买现货（现货买入受现金约束，按比例缩减；永续不占用现金，不缩减）；skipped 期只做收尾。
func (l *Ledger) Rebalance(outcome Outcome, step Step) (Outcome, error) {
	if !l.marked {
		return Outcome{}, errors.New("账本本根还没有估值，调仓前必须先调用 Mark")
	}
	l.marked = false
	return l.rebalance(outcome, step), nil
}

func (l *Ledger) mark(prices, funding map[string]float64) Outcome {
	for _, id := range l.sortedIDs() {
		position := l.Positions[id]
		if price, ok := prices[id]; ok && price > 0 {
			if isSwapKey(id) {
				l.Cash += position.Quantity * (price - position.LastPrice)
			}
			position.LastPrice = price
			position.Frozen = false
			position.MissingBars = 0
		} else {
			position.Frozen = true
			position.MissingBars++
		}
		position.Value = position.Quantity * position.LastPrice
	}
	outcome := Outcome{}
	if funding != nil {
		outcome.Funding = l.settleFunding(funding)
	}
	outcome.EquityBefore = l.Equity()
	// 有持仓（或现金已为负）时权益不超过 0 即爆仓；空账户的零权益不算。
	if !l.Bankrupt && outcome.EquityBefore <= 0 && (len(l.Positions) > 0 || l.Cash < 0) {
		l.Bankrupt, outcome.Bankrupt = true, true
	}
	return outcome
}

// settleFunding 按上一根的快照结算本根的资金费，并记下新快照（本根估值后、调仓前的永续名义价值），返回支出（收入为负）。
// 快照里的仓位在上一根收盘时平掉了也照付；上一根收盘新开的仓位不在快照里，不付。费率缺失或无效时按 0。
func (l *Ledger) settleFunding(funding map[string]float64) float64 {
	paid := 0.0
	for _, key := range l.FundingKeys() {
		rate, ok := funding[key]
		if !ok || math.IsNaN(rate) || math.IsInf(rate, 0) {
			continue
		}
		paid += l.FundingBasis[key] * rate
	}
	l.Cash -= paid
	l.FundingBasis = nil
	for _, id := range l.sortedIDs() {
		if position := l.Positions[id]; isSwapKey(id) && position.Value != 0 {
			if l.FundingBasis == nil {
				l.FundingBasis = make(map[string]float64)
			}
			l.FundingBasis[id] = position.Value
		}
	}
	return paid
}

// FundingKeys 返回下一根结算资金费要读费率的永续键（上一根估值后的快照，含之后平掉的仓位）。
func (l *Ledger) FundingKeys() []string {
	keys := make([]string, 0, len(l.FundingBasis))
	for key := range l.FundingBasis {
		keys = append(keys, key)
	}
	sortKeys(keys)
	return keys
}

func (l *Ledger) rebalance(outcome Outcome, step Step) Outcome {
	dust := dustRatio * math.Max(outcome.EquityBefore, 1)
	if !step.OK {
		outcome.EquityAfter = outcome.EquityBefore
		outcome.SpotValue, outcome.SwapValue = l.values()
		return outcome
	}
	for _, id := range l.sortedIDs() {
		position := l.Positions[id]
		if position.Frozen && l.LiquidateAfter > 0 && position.MissingBars >= l.LiquidateAfter {
			// 现货按最后价卖出换回现金；永续的盈亏已按最后价结算，清算只扣手续费。
			fee := math.Abs(position.Value) * l.feeRate(id)
			if isSwapKey(id) {
				l.Cash -= fee
			} else {
				l.Cash += position.Value - fee
			}
			outcome.Traded += math.Abs(position.Value)
			outcome.Fee += fee
			outcome.Liquidated = append(outcome.Liquidated, id)
			delete(l.Positions, id)
		}
	}
	equity := l.Equity()
	if l.Bankrupt {
		step.Targets = nil
	}
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
		if _, targeted := step.Targets[id]; !targeted && math.Abs(position.Value) > dust {
			unfilled[id] = struct{}{}
		}
	}
	value := func(id string) float64 {
		if position, ok := l.Positions[id]; ok {
			return position.Value
		}
		return 0
	}
	ids := make([]string, 0, len(tradable))
	for id := range tradable {
		ids = append(ids, id)
	}
	sortKeys(ids)
	// 目标金额：现货数量不为负，负权重按空仓处理。
	want := func(id string, adjusted float64) float64 {
		amount := step.Targets[id] * adjusted
		if amount < 0 && !isSwapKey(id) {
			return 0
		}
		return amount
	}
	adjusted := equity
	for iteration := 0; iteration < maxIterations; iteration++ {
		spotCost, swapCost := 0.0, 0.0
		for _, id := range ids {
			if isSwapKey(id) {
				swapCost += math.Abs(want(id, adjusted) - value(id))
			} else {
				spotCost += math.Abs(want(id, adjusted) - value(id))
			}
		}
		next := equity - (l.SpotFeeRate*spotCost + l.SwapFeeRate*swapCost)
		converged := math.Abs(next-adjusted) <= convergenceTolerance*math.Max(math.Abs(equity), 1)
		adjusted = next
		if converged {
			break
		}
	}
	desired := make(map[string]float64, len(ids))
	for _, id := range ids {
		desired[id] = want(id, adjusted)
	}
	for _, id := range ids {
		if isSwapKey(id) {
			continue
		}
		current := value(id)
		if desired[id] >= current-dust {
			continue
		}
		sell := current - desired[id]
		fee := sell * l.SpotFeeRate
		l.Cash += sell - fee
		outcome.Traded += sell
		outcome.Fee += fee
		l.adjust(id, -sell, tradable[id], dust)
	}
	for _, id := range ids {
		if !isSwapKey(id) {
			continue
		}
		if delta := desired[id] - value(id); math.Abs(delta) > dust {
			outcome.Fee += l.tradeSwap(id, delta, tradable[id], dust)
			outcome.Traded += math.Abs(delta)
		}
	}
	buys := make(map[string]float64)
	total := 0.0
	for _, id := range ids {
		if isSwapKey(id) {
			continue
		}
		if amount := desired[id] - value(id); amount > dust {
			buys[id] = amount
			total += amount
		}
	}
	if total > 0 {
		scale := 1.0
		need := total * (1 + l.SpotFeeRate)
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
			fee := buy * l.SpotFeeRate
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
	sortKeys(outcome.Unfilled)
	outcome.EquityAfter = l.Equity()
	outcome.SpotValue, outcome.SwapValue = l.values()
	if outcome.EquityBefore > 0 {
		// 单边换手：成交额（买卖之和）/ 2 / 成交前权益，与实时结果的 Σ|w_new − w_old| / 2 口径一致。
		outcome.Turnover = outcome.Traded / 2 / outcome.EquityBefore
	}
	return outcome
}

// adjust 按金额增减现货持仓（price 为成交价）；剩余价值不超过粉尘阈值时删除持仓。
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

// tradeSwap 按名义金额变化 delta（正为加多或减空，负为加空或减多）调整永续持仓，返回手续费：成交价就是已结算的当期价，
// 只改数量、扣手续费。
func (l *Ledger) tradeSwap(id string, delta, price, dust float64) float64 {
	position, ok := l.Positions[id]
	if !ok {
		position = &Position{LastPrice: price}
		l.Positions[id] = position
	}
	fee := math.Abs(delta) * l.SwapFeeRate
	l.Cash -= fee
	position.LastPrice = price
	position.Quantity += delta / price
	position.Value = position.Quantity * price
	if math.Abs(position.Value) <= dust {
		delete(l.Positions, id)
	}
	return fee
}

func (l *Ledger) sortedIDs() []string {
	ids := make([]string, 0, len(l.Positions))
	for id := range l.Positions {
		ids = append(ids, id)
	}
	sortKeys(ids)
	return ids
}

// sortKeys 先按标的、再按市场排序（现货类在前，永续其次）；没有市场后缀的键视为现货类。
func sortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		idA, _ := engine.SplitTargetKey(keys[i])
		idB, _ := engine.SplitTargetKey(keys[j])
		if idA != idB {
			return idA < idB
		}
		return !isSwapKey(keys[i]) && isSwapKey(keys[j])
	})
}
