package target

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// laneEnv 是一轮收敛里各标的共用的下单环境：下单设置、报价、权益和本小时已用额度都在一轮里只取一次，
// Converge、HasDifference（过期告警）和待提交子单的重新判断用同一份，结论一致。
type laneEnv struct {
	settings ordersettings.Settings
	// quotes 是按需预取的标的报价（键：内部标的 ID）；阈值模式要用金额时才取，取不到的标的没有条目。
	quotes map[string]Quote
	// equity 是组合最近一个权益采样点的权益；equityKnown 为假表示取不到，EQUITY_RATIO 阈值不生效。
	equity      shared.Decimal
	equityKnown bool
}

// errOrderSettingsUnreadable 表示下单设置读不出来：这个组合不下单，原因写进目标。
var errOrderSettingsUnreadable = errors.New("下单设置无法解析")

// loadOrderSettings 读取并严格解析组合的下单设置。保存时已经校验过，读不出来说明数据被改坏。
func loadOrderSettings(logical store.LogicalAccountRecord) (ordersettings.Settings, error) {
	settings, err := ordersettings.Parse(logical.OrderSettingsJSON)
	if err != nil {
		return ordersettings.Settings{}, fmt.Errorf("%w：组合 %s：%v", errOrderSettingsUnreadable, logical.LogicalAccountID, err)
	}
	return settings, nil
}

// newLaneEnv 建立这一轮的下单环境。
func (e *Executor) newLaneEnv(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	settings ordersettings.Settings,
	members []memberState,
	desired map[string]desiredTarget,
) *laneEnv {
	env := &laneEnv{settings: settings, quotes: make(map[string]Quote)}
	needsValue := settings.ThresholdMode == ordersettings.ThresholdPositionRatio ||
		settings.ThresholdMode == ordersettings.ThresholdEquityRatio
	if !needsValue {
		return env
	}
	if settings.ThresholdMode == ordersettings.ThresholdEquityRatio {
		// 没有采样点（新组合）时取不到权益，阈值不生效。
		if point, err := e.Store.LatestLogicalAccountEquityPoint(ctx, spaceID, logicalAccountID); err == nil {
			if equity, parseErr := shared.ParseDecimal(point.Equity); parseErr == nil && equity.Cmp(shared.Zero()) > 0 {
				env.equity, env.equityKnown = equity, true
			}
		}
	}
	// 只有目标和持仓都不为 0 的标的才会用到阈值，其余标的不取报价。
	for _, instrumentID := range laneIDs(desired, members) {
		target, ok := desired[instrumentID]
		if !ok || target.Hold || target.Quantity.IsZero() {
			continue
		}
		for _, member := range members {
			if member.positions[instrumentID].IsZero() {
				continue
			}
			instrument, known := member.instruments[instrumentID]
			if !known {
				continue
			}
			quote, err := e.Prices.LatestPrice(ctx, member.account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
			if err == nil && quote.Price.Cmp(shared.Zero()) > 0 {
				env.quotes[instrumentID] = quote
			}
			break
		}
	}
	return env
}

// withinRebalanceThreshold 判断整条标的的差额是否小到不用调。四种模式：
//   - TARGET_RATIO：|目标 − 合计持仓| 不超过 |目标| × r；
//   - POSITION_RATIO：调仓金额低于当前持仓金额 × r；
//   - EQUITY_RATIO：调仓金额低于组合权益 × r；
//   - ALWAYS：每期都调。
//
// 建仓（合计持仓为 0）和清仓（目标为 0）在任何模式下都不受阈值限制；只要有成员持仓和目标反向，也不跳过。
// 按整条标的比较，反向持仓和多成员对冲不会被跳过。需要金额而取不到报价（或权益）时不跳过，宁可多调。
func (env *laneEnv) withinRebalanceThreshold(
	desired shared.Decimal,
	confirmed shared.Decimal,
	members []memberState,
	instrumentID string,
) bool {
	settings := env.settings
	if settings.ThresholdMode == ordersettings.ThresholdAlways {
		return false
	}
	if desired.IsZero() || confirmed.IsZero() {
		return false
	}
	for _, member := range members {
		position := member.positions[instrumentID]
		if !position.IsZero() && !sameSign(position, desired) {
			return false
		}
	}
	diff := desired.Sub(confirmed).Abs()
	switch settings.ThresholdMode {
	case ordersettings.ThresholdTargetRatio:
		return diff.Cmp(desired.Abs().Mul(settings.ThresholdRatio)) <= 0
	case ordersettings.ThresholdPositionRatio:
		quote, ok := env.quotes[instrumentID]
		if !ok {
			return false
		}
		return diff.Mul(quote.Price).Cmp(confirmed.Abs().Mul(quote.Price).Mul(settings.ThresholdRatio)) < 0
	case ordersettings.ThresholdEquityRatio:
		quote, ok := env.quotes[instrumentID]
		if !ok || !env.equityKnown {
			return false
		}
		return diff.Mul(quote.Price).Cmp(env.equity.Mul(settings.ThresholdRatio)) < 0
	}
	return false
}

// belowConfiguredMinimum 判断子单金额是否低于设置的最小下单额（现货、合约各一个值）。
// 合约的减仓和清仓不受它限制：低于门槛的剩余仓位也要能平掉。交易所自己的最小名义金额另行判断，
// OKX 没有最小名义金额，它的最小数量（minSz）在取整时已经保证金额不低于 minSz × 价格。
func (env *laneEnv) belowConfiguredMinimum(candidate laneAction, quantity, price shared.Decimal) bool {
	if candidate.reducing && exchange.RuleFor(candidate.instrument.MarketType).ReduceOnly {
		return false
	}
	minimum := env.settings.MinNotional(candidate.instrument.MarketType)
	return minimum.Cmp(shared.Zero()) > 0 && quantity.Mul(price).Cmp(minimum) < 0
}
