package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// ReservationRequest 描述计算订单预留与模拟成交参数所需的输入。
type ReservationRequest struct {
	Account          store.TradingAccountRecord
	Instrument       exchange.Instrument
	OrderType        exchange.OrderType
	Side             exchange.Side
	Quantity         shared.Decimal
	ReferencePrice   shared.Decimal
	LimitPrice       *shared.Decimal
	ReduceOnly       bool
	Leverage         shared.Decimal
	DefaultFeeBuffer shared.Decimal
}

// ReservationResult 是订单预留计算结果；ExecutionPrice 与 FirstMatchPending 只对本地撮合适配器有值。
type ReservationResult struct {
	ReservedAsset       string
	ReservedQuantity    shared.Decimal
	ExecutionPrice      *shared.Decimal
	FirstMatchPending   bool
	DefaultLeverageUsed bool
	Leverage            shared.Decimal
}

// RepriceRequest 描述补取新鲜报价后重算市价单预留与执行价的输入。
type RepriceRequest struct {
	Account                store.TradingAccountRecord
	Instrument             exchange.Instrument
	Side                   exchange.Side
	Quantity               shared.Decimal
	NewReferencePrice      shared.Decimal
	OldReferencePrice      shared.Decimal
	PreviousExecutionPrice *shared.Decimal
	ReduceOnly             bool
	Leverage               shared.Decimal
	DefaultFeeBuffer       shared.Decimal
}

// CapacityClampRequest 描述整手/阶梯费用市场按可用资金收紧下单容量的输入。
type CapacityClampRequest struct {
	Account        store.TradingAccountRecord
	Instrument     exchange.Instrument
	OrderType      exchange.OrderType
	Side           exchange.Side
	ReferencePrice shared.Decimal
	LimitPrice     *shared.Decimal
	Quantity       shared.Decimal
	Step           shared.Decimal
	Available      shared.Decimal
}

// OrderPolicy 收口各执行模式（实盘 / 模拟盘）在报价准备、滑点预留、容量收紧、本地资金账本与本地撤单确认上的差异，
// 避免上层 order / target 服务散落 ExecutionModePaper 特判。
type OrderPolicy interface {
	// NeedsExecutableQuote 报告下单与容量估算前是否需向适配器现查买卖一档可执行价。
	NeedsExecutableQuote() bool
	// ResolveExecutableQuote 读取方向可执行价并校验新鲜度。
	ResolveExecutableQuote(ctx context.Context, key exchange.SymbolKey, side exchange.Side, now time.Time, maxAge time.Duration) (shared.Decimal, time.Time, error)
	// UsesLocalFundsLedger 报告资金预留检查是否走本地模拟余额账本而非交易所快照。
	UsesLocalFundsLedger() bool
	// ConfirmsCancelLocally 报告撤单成功回执后是否由本地直接确认终态。
	ConfirmsCancelLocally() bool
	// NeedsMarginPoolSnapshot 报告该订单预留是否与合约保证金共用模拟盘单一资金池。
	NeedsMarginPoolSnapshot(account store.TradingAccountRecord, reservedAsset string, reduceOnly bool) bool
	// MarginPoolAdjustment 按统一公式计算模拟盘单一资金池下的保证金可用额调整。
	MarginPoolAdjustment(market exchange.MarketType, usedMargin, unrealizedPnL shared.Decimal) shared.Decimal
	// ComputeReservation 按适配器的费率、滑点与保证金规则计算预留资产、预留数量与首撮参数。
	ComputeReservation(req ReservationRequest) (ReservationResult, error)
	// RepriceMarketOrder 在重取新鲜报价后保持原滑点比例重算市价单执行价与预留数量。
	RepriceMarketOrder(req RepriceRequest) (executionPrice shared.Decimal, reservedQuantity shared.Decimal, err error)
	// ClampBuyCapacity 在自定义费用函数（如最低佣金、印花税）场景下把可买数量收到付得起。
	ClampBuyCapacity(req CapacityClampRequest) shared.Decimal
	// DefaultLeverage 返回未显式配置杠杆时的默认杠杆（仅模拟盘有保守默认值 1x）。
	DefaultLeverage() (shared.Decimal, bool)
}

// ResolveOrderPolicy 按账户执行模式返回对应的订单策略（实盘或模拟盘）。
func ResolveOrderPolicy(adapter ExecutionAdapter, account store.TradingAccountRecord) OrderPolicy {
	if account.ExecutionMode == string(exchange.ExecutionModePaper) {
		return PaperOrderPolicy{Adapter: adapter}
	}
	return LiveOrderPolicy{Adapter: adapter}
}

// resolveExecutableQuote 从适配器读取方向可执行价并校验新鲜度，实盘与模拟盘共用。
func resolveExecutableQuote(
	ctx context.Context,
	adapter ExecutionAdapter,
	key exchange.SymbolKey,
	side exchange.Side,
	now time.Time,
	maxAge time.Duration,
) (shared.Decimal, time.Time, error) {
	if adapter == nil {
		return shared.Decimal{}, time.Time{}, errors.New("execution: adapter is nil")
	}
	quote, err := adapter.GetQuote(ctx, key)
	if err != nil {
		return shared.Decimal{}, time.Time{}, err
	}
	if !QuoteWithinAge(quote, now, maxAge) {
		return shared.Decimal{}, time.Time{}, fmt.Errorf("execution: quote is stale")
	}
	price, err := ExecutableQuotePrice(side, quote)
	if err != nil {
		return shared.Decimal{}, time.Time{}, err
	}
	return price, quote.SourceTime, nil
}

// ApplySlippage 是全模块唯一的滑点计算公式：买入放大 (1 + bps/10000)，卖出缩小 (1 - bps/10000)。
func ApplySlippage(price shared.Decimal, side exchange.Side, slippageBPS shared.Decimal) shared.Decimal {
	if slippageBPS.Cmp(shared.Zero()) <= 0 {
		return price
	}
	factor := shared.MustDecimal("1").Add(slippageBPS.Div(shared.MustDecimal("10000")))
	if side == exchange.SideSell {
		factor = shared.MustDecimal("1").Sub(slippageBPS.Div(shared.MustDecimal("10000")))
	}
	return price.Mul(factor)
}

// ExecutableQuotePrice 从盘口提取方向可执行价（买取卖一、卖取买一，缺失回退最新价）。
func ExecutableQuotePrice(side exchange.Side, quote MarketQuote) (shared.Decimal, error) {
	if side != exchange.SideBuy && side != exchange.SideSell {
		return shared.Decimal{}, errors.New("trade order: order side is required")
	}
	price := quote.Ask
	if side == exchange.SideSell {
		price = quote.Bid
	}
	if price.IsZero() {
		price = quote.Last
	}
	if price.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, errors.New("trade order: executable quote is empty")
	}
	return price, nil
}

// QuoteWithinAge 判断下单报价时间戳是否在允许的时延窗口内。
func QuoteWithinAge(quote MarketQuote, now time.Time, maxAge time.Duration) bool {
	if quote.SourceTime.IsZero() {
		return false
	}
	if maxAge <= 0 {
		return !quote.SourceTime.After(now)
	}
	age := now.Sub(quote.SourceTime)
	return age >= 0 && age <= maxAge
}

// LiveOrderPolicy 是实盘适配器的标准策略实现。
type LiveOrderPolicy struct {
	Adapter ExecutionAdapter
}

func (LiveOrderPolicy) NeedsExecutableQuote() bool { return false }

func (p LiveOrderPolicy) ResolveExecutableQuote(ctx context.Context, key exchange.SymbolKey, side exchange.Side, now time.Time, maxAge time.Duration) (shared.Decimal, time.Time, error) {
	return resolveExecutableQuote(ctx, p.Adapter, key, side, now, maxAge)
}

func (LiveOrderPolicy) UsesLocalFundsLedger() bool  { return false }
func (LiveOrderPolicy) ConfirmsCancelLocally() bool { return false }
func (LiveOrderPolicy) NeedsMarginPoolSnapshot(store.TradingAccountRecord, string, bool) bool {
	return false
}
func (LiveOrderPolicy) MarginPoolAdjustment(exchange.MarketType, shared.Decimal, shared.Decimal) shared.Decimal {
	return shared.Zero()
}
func (LiveOrderPolicy) DefaultLeverage() (shared.Decimal, bool) { return shared.Zero(), false }

func (LiveOrderPolicy) ComputeReservation(req ReservationRequest) (ReservationResult, error) {
	referenceNotional := req.Quantity.Mul(req.ReferencePrice)
	orderNotional := referenceNotional
	if req.OrderType == exchange.OrderTypeLimit && req.LimitPrice != nil {
		orderNotional = req.Quantity.Mul(*req.LimitPrice)
	}
	result := ReservationResult{Leverage: req.Leverage}
	if rule := exchange.RuleFor(req.Instrument.MarketType); !rule.UsesMargin {
		if req.Side == exchange.SideBuy {
			result.ReservedAsset = req.Instrument.QuoteAsset
			if req.OrderType == exchange.OrderTypeLimit {
				result.ReservedQuantity = orderNotional
			} else {
				result.ReservedQuantity = withFeeBuffer(referenceNotional, req.DefaultFeeBuffer)
			}
		} else {
			result.ReservedAsset = req.Instrument.BaseAsset
			result.ReservedQuantity = req.Quantity
		}
		return result, nil
	}
	result.ReservedAsset = req.Account.SettlementAsset
	if req.ReduceOnly {
		return result, nil
	}
	margin := referenceNotional.Div(req.Leverage)
	result.ReservedQuantity = withFeeBuffer(margin, req.DefaultFeeBuffer)
	return result, nil
}

func (LiveOrderPolicy) RepriceMarketOrder(req RepriceRequest) (shared.Decimal, shared.Decimal, error) {
	return req.NewReferencePrice, shared.Zero(), nil
}

func (LiveOrderPolicy) ClampBuyCapacity(req CapacityClampRequest) shared.Decimal {
	return req.Quantity
}

// PaperOrderPolicy 是模拟盘适配器的标准订单预留、滑点与容量策略。
type PaperOrderPolicy struct {
	Adapter ExecutionAdapter
}

func (PaperOrderPolicy) NeedsExecutableQuote() bool { return true }

func (p PaperOrderPolicy) ResolveExecutableQuote(ctx context.Context, key exchange.SymbolKey, side exchange.Side, now time.Time, maxAge time.Duration) (shared.Decimal, time.Time, error) {
	return resolveExecutableQuote(ctx, p.Adapter, key, side, now, maxAge)
}

func (PaperOrderPolicy) UsesLocalFundsLedger() bool  { return true }
func (PaperOrderPolicy) ConfirmsCancelLocally() bool { return true }

func (PaperOrderPolicy) NeedsMarginPoolSnapshot(account store.TradingAccountRecord, reservedAsset string, reduceOnly bool) bool {
	return !reduceOnly && account.HasLeverageMarket() && reservedAsset == account.SettlementAsset
}

func (PaperOrderPolicy) MarginPoolAdjustment(market exchange.MarketType, usedMargin, unrealizedPnL shared.Decimal) shared.Decimal {
	return tradingaccount.PaperMarginPoolAdjustment(exchange.RuleFor(market).UsesMargin, usedMargin, unrealizedPnL)
}

func (PaperOrderPolicy) DefaultLeverage() (shared.Decimal, bool) {
	return shared.MustDecimal("1"), true
}

func (PaperOrderPolicy) ComputeReservation(req ReservationRequest) (ReservationResult, error) {
	referenceNotional := req.Quantity.Mul(req.ReferencePrice)
	orderNotional := referenceNotional
	if req.OrderType == exchange.OrderTypeLimit && req.LimitPrice != nil {
		orderNotional = req.Quantity.Mul(*req.LimitPrice)
	}
	feeRate := req.DefaultFeeBuffer
	_, maker, taker, slippage, hasPaper := req.Account.PaperDecimals()
	if hasPaper {
		feeRate = maxDecimal(maker, taker)
	}
	result := ReservationResult{
		Leverage:          req.Leverage,
		FirstMatchPending: req.OrderType == exchange.OrderTypeMarket || req.OrderType == exchange.OrderTypeLimit,
	}
	if req.OrderType == exchange.OrderTypeMarket {
		execPrice := req.ReferencePrice
		if hasPaper {
			execPrice = ApplySlippage(execPrice, req.Side, slippage)
		}
		result.ExecutionPrice = &execPrice
	}
	if rule := exchange.RuleFor(req.Instrument.MarketType); !rule.UsesMargin {
		if req.Side == exchange.SideBuy {
			result.ReservedAsset = req.Instrument.QuoteAsset
			reserveNotional := referenceNotional
			if req.OrderType == exchange.OrderTypeLimit {
				reserveNotional = orderNotional
			} else if hasPaper {
				reserveNotional = ApplySlippage(referenceNotional, req.Side, slippage)
			}
			result.ReservedQuantity = paperBuyReservation(req.Account, req.Instrument.MarketType, reserveNotional, feeRate)
		} else {
			result.ReservedAsset = req.Instrument.BaseAsset
			result.ReservedQuantity = req.Quantity
		}
		return result, nil
	}
	result.ReservedAsset = req.Account.SettlementAsset
	reserveNotional := referenceNotional
	if req.OrderType == exchange.OrderTypeLimit && req.LimitPrice != nil {
		reserveNotional = orderNotional
	} else if req.OrderType == exchange.OrderTypeMarket && hasPaper {
		reserveNotional = ApplySlippage(referenceNotional, req.Side, slippage)
	}
	if req.ReduceOnly {
		result.ReservedQuantity = reserveNotional.Mul(feeRate)
	} else {
		margin := reserveNotional.Div(req.Leverage)
		result.ReservedQuantity = margin.Add(reserveNotional.Mul(feeRate))
	}
	return result, nil
}

func (PaperOrderPolicy) RepriceMarketOrder(req RepriceRequest) (shared.Decimal, shared.Decimal, error) {
	price := req.NewReferencePrice
	_, maker, taker, slippage, hasPaper := req.Account.PaperDecimals()
	if req.PreviousExecutionPrice != nil {
		if req.OldReferencePrice.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{}, errors.New("execution: invalid previous reference price")
		}
		// 保留建单时冻结的滑点比例，即使账户滑点参数之后变更也不漂移。
		price = req.PreviousExecutionPrice.Mul(price).Div(req.OldReferencePrice)
	} else if hasPaper {
		price = ApplySlippage(price, req.Side, slippage)
	}
	feeRate := req.DefaultFeeBuffer
	if hasPaper {
		feeRate = maxDecimal(maker, taker)
	}
	notional := req.Quantity.Mul(price)
	var reserved shared.Decimal
	if exchange.RuleFor(req.Instrument.MarketType).UsesMargin {
		reserved = notional.Mul(feeRate)
		if !req.ReduceOnly {
			reserved = reserved.Add(notional.Div(req.Leverage))
		}
	} else if req.Side == exchange.SideBuy {
		reserved = paperBuyReservation(req.Account, req.Instrument.MarketType, notional, feeRate)
	}
	return price, reserved, nil
}

func (PaperOrderPolicy) ClampBuyCapacity(req CapacityClampRequest) shared.Decimal {
	rule := exchange.RuleAt(exchange.Exchange(req.Account.Exchange), req.Instrument.MarketType)
	_, _, taker, slippage, hasPaper := req.Account.PaperDecimals()
	if req.Side != exchange.SideBuy || !hasPaper || rule.Fee == nil {
		return req.Quantity
	}
	unit := req.ReferencePrice
	if req.OrderType == exchange.OrderTypeLimit && req.LimitPrice != nil {
		unit = *req.LimitPrice
	} else {
		unit = ApplySlippage(unit, req.Side, slippage)
	}
	quantity := req.Quantity
	for quantity.Cmp(shared.Zero()) > 0 &&
		rule.BuyCost(quantity.Mul(unit), taker).Cmp(req.Available) > 0 {
		quantity = quantity.Sub(req.Step)
	}
	if quantity.IsNegative() {
		return shared.Zero()
	}
	return quantity
}

func paperBuyReservation(account store.TradingAccountRecord, market exchange.MarketType, notional, feeRate shared.Decimal) shared.Decimal {
	if _, _, taker, _, ok := account.PaperDecimals(); ok {
		if rule := exchange.RuleAt(exchange.Exchange(account.Exchange), market); rule.Fee != nil {
			return rule.BuyCost(notional, taker)
		}
	}
	return withFeeBuffer(notional, feeRate)
}

func withFeeBuffer(amount, rate shared.Decimal) shared.Decimal {
	if rate.Cmp(shared.Zero()) <= 0 {
		return amount
	}
	return amount.Add(amount.Mul(rate))
}

func maxDecimal(left, right shared.Decimal) shared.Decimal {
	if left.Cmp(right) >= 0 {
		return left
	}
	return right
}
