package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var (
	ErrAccountOwnership   = errors.New("执行账户不属于当前空间")
	ErrInstrumentDisabled = errors.New("标的不可交易")
	ErrQuantityRule       = errors.New("数量不符合交易规则")
	ErrNotionalLimit      = errors.New("超过单笔子单金额上限")
	ErrInsufficientFunds  = errors.New("可用资金不足")
	ErrLeverageLimit      = errors.New("杠杆超过上限")
	ErrReduceOnly         = errors.New("只减仓订单无效")
	ErrValidatorConfig    = errors.New("下单校验未配置")
	// ErrInstrumentHalted 表示标的停牌（或已退市）。不是永久错误：执行器把它当成顺延，复牌后照常下单。
	ErrInstrumentHalted = errors.New("停牌")
	// ErrPriceLimit 表示委托价超出涨跌停价。执行器下单前已经把价格截在涨跌停价之间，正常路径不会遇到。
	ErrPriceLimit = errors.New("超出涨跌停价")
)

// QuoteSource 给有涨跌停的市场（A 股）取实时报价，用来拦停牌和超出涨跌停价的委托。
type QuoteSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

type AccountEligibility interface {
	ExecutionEligibility(context.Context, string) (store.TradingAccountRecord, error)
}

type InstrumentSource interface {
	GetInstrument(context.Context, exchange.Exchange, exchange.MarketType, string) (exchange.Instrument, error)
}

// AccountInstrumentSource lets production implementations include the
// account's environment in instrument identity without burdening test stubs.
type AccountInstrumentSource interface {
	GetInstrumentForAccount(context.Context, string, exchange.Exchange, exchange.MarketType, string) (exchange.Instrument, error)
}

type PositionSource interface {
	GetPosition(context.Context, string, string) (exchange.Position, error)
}

type AccountPositionSource interface {
	GetPositionForAccount(context.Context, string, string) (exchange.Position, error)
}

type Validation struct {
	Account           store.TradingAccountRecord
	Instrument        exchange.Instrument
	Notional          shared.Decimal
	Leverage          shared.Decimal
	ReservedAsset     string
	ReservedQuantity  shared.Decimal
	ExecutionPrice    *shared.Decimal
	FirstMatchPending bool
	Policy            execution.OrderPolicy
	// ReduceOnly：减仓单只预留手续费，不占用新保证金。
	ReduceOnly bool
}

type Validator struct {
	Accounts         AccountEligibility
	Instruments      InstrumentSource
	Positions        PositionSource
	Now              func() time.Time
	MaxReferenceAge  time.Duration
	MaxChildNotional shared.Decimal
	MaxLeverage      shared.Decimal
	FeeBufferRate    shared.Decimal
	// Quotes 为空时不检查停牌和涨跌停（只在单元测试里）。
	Quotes QuoteSource
}

func (v Validator) Validate(
	ctx context.Context,
	spaceID string,
	spec orderdomain.OrderSpec,
) (Validation, error) {
	return v.validate(ctx, spaceID, spec, false)
}

// OrderService checks Paper funds against the ledger and reservations in its
// write transaction; a display snapshot must not reject or authorize that write.
func (v Validator) validate(ctx context.Context, spaceID string, spec orderdomain.OrderSpec, deferPaperFunds bool) (Validation, error) {
	return v.validateOptions(ctx, spaceID, spec, deferPaperFunds, false)
}

// Capacity quotes reuse reservation pricing before enforcing the final size.
func (v Validator) validateOptions(ctx context.Context, spaceID string, spec orderdomain.OrderSpec, deferPaperFunds, capacity bool) (Validation, error) {
	if v.Accounts == nil || v.Instruments == nil || strings.TrimSpace(spaceID) == "" {
		return Validation{}, ErrValidatorConfig
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	account, err := v.Accounts.ExecutionEligibility(ctx, spec.TradingAccountID)
	if err != nil {
		return Validation{}, err
	}
	if account.SpaceID != spaceID {
		return Validation{}, ErrAccountOwnership
	}
	// 订单的市场由标的 ID 决定，必须是账户已开通的市场。
	market := account.MarketForInstrument(spec.InstrumentID)
	if market == "" {
		return Validation{}, fmt.Errorf("%w: 标的 %s 缺少市场后缀 -SPOT 或 -SWAP", ErrInstrumentDisabled, spec.InstrumentID)
	}
	if !account.SupportsMarket(string(market)) {
		return Validation{}, fmt.Errorf("%w: 账户没有开通市场 %s", ErrInstrumentDisabled, market)
	}
	if err := spec.Validate(market, now, v.MaxReferenceAge); err != nil {
		return Validation{}, err
	}
	var instrument exchange.Instrument
	if scoped, ok := v.Instruments.(AccountInstrumentSource); ok {
		instrument, err = scoped.GetInstrumentForAccount(ctx, account.TradingAccountID, exchange.Exchange(account.Exchange), market, spec.InstrumentID)
	} else {
		instrument, err = v.Instruments.GetInstrument(ctx, exchange.Exchange(account.Exchange), market, spec.InstrumentID)
	}
	if err != nil {
		return Validation{}, err
	}
	if !exchange.StatusTradable(instrument.Status) {
		return Validation{}, ErrInstrumentDisabled
	}
	exchangeQuantity := spec.Quantity
	if exchange.RuleFor(market).ContractValue {
		if instrument.ContractValue.Cmp(shared.Zero()) <= 0 {
			return Validation{}, ErrQuantityRule
		}
		exchangeQuantity = spec.Quantity.Div(instrument.ContractValue)
	}
	if !capacity && !validQuantity(
		exchangeQuantity,
		instrument.ExchangeQuantityStep,
		instrument.MinExchangeQuantity,
	) && !oddLotSell(account, market, instrument, spec) {
		return Validation{}, ErrQuantityRule
	}
	if !capacity && exchange.RuleFor(market).QuoteGuards {
		if err := v.checkPriceLimits(ctx, account, instrument, spec); err != nil {
			return Validation{}, err
		}
	}

	referenceNotional := spec.Quantity.Mul(spec.ReferencePrice)
	orderNotional := referenceNotional
	if spec.Type == exchange.OrderTypeLimit {
		orderNotional = spec.Quantity.Mul(*spec.LimitPrice)
	}
	// 合约全平只查最小数量。
	// 最小名义金额：交易所给了就用它，没给（OKX）用 minSz × 合约面值 × 价格折算。
	orderPrice := spec.ReferencePrice
	if spec.Type == exchange.OrderTypeLimit {
		orderPrice = *spec.LimitPrice
	}
	if minNotional := instrument.MinNotionalAt(orderPrice); !capacity && !spec.ReducePositionOnly &&
		minNotional.Cmp(shared.Zero()) > 0 && orderNotional.Cmp(minNotional) < 0 {
		return Validation{}, ErrQuantityRule
	}
	if !capacity && v.MaxChildNotional.Cmp(shared.Zero()) > 0 &&
		referenceNotional.Cmp(v.MaxChildNotional) > 0 {
		return Validation{}, ErrNotionalLimit
	}

	var adapter execution.ExecutionAdapter
	if v.Quotes != nil {
		if resolved, adapterErr := v.Quotes.Adapter(account.TradingAccountID); adapterErr == nil {
			adapter = resolved
		}
	}
	policy := execution.ResolveOrderPolicy(adapter, account)

	result := Validation{
		Account:    account,
		Instrument: instrument,
		Notional:   referenceNotional,
		Leverage:   shared.MustDecimal("1"),
		Policy:     policy,
	}
	if rule := exchange.RuleFor(market); rule.UsesMargin {
		leverage, found := account.LeverageDecimal(spec.InstrumentID)
		if !found {
			leverage, found = account.LeverageDecimal(store.DefaultLeverageKey)
		}
		if !found {
			leverage, found = policy.DefaultLeverage()
		}
		if !found || leverage.Cmp(shared.Zero()) <= 0 {
			return Validation{}, fmt.Errorf("%w：该标的没有配置杠杆", ErrLeverageLimit)
		}
		if v.MaxLeverage.Cmp(shared.Zero()) > 0 && leverage.Cmp(v.MaxLeverage) > 0 {
			return Validation{}, ErrLeverageLimit
		}
		result.Leverage = leverage
		if spec.ReducePositionOnly {
			if err := v.validateReduceOnly(ctx, spec); err != nil {
				return Validation{}, err
			}
			result.ReduceOnly = true
		}
	}
	reservation, err := policy.ComputeReservation(execution.ReservationRequest{
		Account:          account,
		Instrument:       instrument,
		OrderType:        spec.Type,
		Side:             spec.Side,
		Quantity:         spec.Quantity,
		ReferencePrice:   spec.ReferencePrice,
		LimitPrice:       spec.LimitPrice,
		ReduceOnly:       result.ReduceOnly,
		Leverage:         result.Leverage,
		DefaultFeeBuffer: v.FeeBufferRate,
	})
	if err != nil {
		return Validation{}, err
	}
	result.ReservedAsset = reservation.ReservedAsset
	result.ReservedQuantity = reservation.ReservedQuantity
	result.ExecutionPrice = reservation.ExecutionPrice
	result.FirstMatchPending = reservation.FirstMatchPending
	if !capacity && !(deferPaperFunds && policy.UsesLocalFundsLedger()) &&
		!result.ReservedQuantity.IsZero() &&
		availableFunds(account, market, result.ReservedAsset).Cmp(result.ReservedQuantity) < 0 {
		return Validation{}, ErrInsufficientFunds
	}
	return result, nil
}

func (v Validator) validateReduceOnly(ctx context.Context, spec orderdomain.OrderSpec) error {
	if v.Positions == nil {
		return ErrReduceOnly
	}
	var position exchange.Position
	var err error
	if scoped, ok := v.Positions.(AccountPositionSource); ok {
		position, err = scoped.GetPositionForAccount(ctx, spec.TradingAccountID, spec.InstrumentID)
	} else {
		position, err = v.Positions.GetPosition(ctx, spec.TradingAccountID, spec.InstrumentID)
	}
	if err != nil {
		return err
	}
	if position.SignedQuantity.IsZero() ||
		(position.SignedQuantity.Cmp(shared.Zero()) > 0 && spec.Side != exchange.SideSell) ||
		(position.SignedQuantity.Cmp(shared.Zero()) < 0 && spec.Side != exchange.SideBuy) {
		return ErrReduceOnly
	}
	if spec.Quantity.Cmp(position.SignedQuantity.Abs()) > 0 {
		return ErrReduceOnly
	}
	return nil
}

// oddLotSell 报告一笔不满足整手规则的卖单是不是合规的零股卖出：只对整手交易的市场（股票），
// 卖出后余下的持仓要么为 0，要么本身满足整手规则——也就是不足一手的零股一次卖完。
func oddLotSell(account store.TradingAccountRecord, market exchange.MarketType, instrument exchange.Instrument, spec orderdomain.OrderSpec) bool {
	if !exchange.RuleFor(market).BoardLot || spec.Side != exchange.SideSell || spec.Quantity.Cmp(shared.Zero()) <= 0 ||
		!spec.Quantity.IsInteger() {
		return false
	}
	remaining := totalBalance(account.Snapshot.ToExchange(), instrument.BaseAsset).Sub(spec.Quantity)
	if remaining.IsNegative() {
		return false
	}
	return remaining.IsZero() || validQuantity(remaining, instrument.ExchangeQuantityStep, instrument.MinExchangeQuantity)
}

// totalBalance 加总资产在各钱包的总额。
func totalBalance(snapshot exchange.AccountSnapshot, asset string) shared.Decimal {
	total := shared.Zero()
	for _, balance := range snapshot.Balances {
		if balance.Asset == asset {
			total = total.Add(balance.Total)
		}
	}
	return total
}

// checkPriceLimits 给报价带停牌和涨跌停的市场（股票）查实时报价：停牌返回 ErrInstrumentHalted，限价超出涨跌停价返回 ErrPriceLimit
// （港股的报价没有涨跌停价，只查停牌）。
func (v Validator) checkPriceLimits(ctx context.Context, account store.TradingAccountRecord, instrument exchange.Instrument, spec orderdomain.OrderSpec) error {
	if v.Quotes == nil {
		return nil
	}
	adapter, err := v.Quotes.Adapter(account.TradingAccountID)
	if err != nil {
		return err
	}
	quote, err := adapter.GetQuote(ctx, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
	if err != nil {
		return err
	}
	if quote.Halted {
		return fmt.Errorf("%w：%s", ErrInstrumentHalted, instrument.InstrumentID)
	}
	if spec.Type != exchange.OrderTypeLimit || spec.LimitPrice == nil {
		return nil
	}
	price := *spec.LimitPrice
	if (quote.LimitUp.Cmp(shared.Zero()) > 0 && price.Cmp(quote.LimitUp) > 0) ||
		(quote.LimitDown.Cmp(shared.Zero()) > 0 && price.Cmp(quote.LimitDown) < 0) {
		return fmt.Errorf("%w：委托价 %s，涨停 %s、跌停 %s", ErrPriceLimit, price, quote.LimitUp, quote.LimitDown)
	}
	return nil
}

func validQuantity(quantity, step, minimum shared.Decimal) bool {
	if step.Cmp(shared.Zero()) <= 0 || quantity.Cmp(minimum) < 0 {
		return false
	}
	return quantity.Div(step).IsInteger()
}

// availableFunds 返回订单所在钱包里某个资产的可用资金。普通账户的现货、合约钱包分开算；
// 统一账户的合约和 OKX、模拟盘共用一个钱包，保证金按账户级可用资金算；只开通合约的账户同样如此。
func availableFunds(account store.TradingAccountRecord, market exchange.MarketType, asset string) shared.Decimal {
	snapshot := account.Snapshot.ToExchange()
	// 统一账户、OKX、模拟盘各市场共用一个资金池：开通了合约时结算币看账户级可用资金（已扣保证金），其他资产加总各钱包。
	if account.SharesFundsAcrossMarkets() {
		// 未核对（实盘前 V3 实测确认）：币安统一账户的现货（杠杆账户）买单是否只能用杠杆钱包里的结算币。
		// 官方文档只写了 /papi/v1/asset-collection 把资产“从期货账户转到杠杆账户”，没有写明杠杆下单不能直接用 UM 钱包；
		// 若实测确认只能用杠杆钱包，这里要改看杠杆钱包可用额，资金不足时先归集再下单。
		if account.HasLeverageMarket() && asset == account.SettlementAsset {
			return snapshot.AvailableFunds
		}
		return availablePool(snapshot, asset)
	}
	wallet := account.WalletFor(string(market))
	if exchange.RuleFor(market).UsesMargin && (len(account.Markets) == 1 || wallet == exchange.WalletUnified) {
		return snapshot.AvailableFunds
	}
	return availableBalance(snapshot, wallet, asset)
}

// availablePool 加总资产在各钱包的可用余额。
func availablePool(snapshot exchange.AccountSnapshot, asset string) shared.Decimal {
	total := shared.Zero()
	for _, balance := range snapshot.Balances {
		if balance.Asset == asset {
			total = total.Add(balance.Available)
		}
	}
	return total
}

// availableBalance 取指定钱包里资产的可用余额；快照没有钱包信息（空钱包）的余额当作属于任何钱包。
func availableBalance(snapshot exchange.AccountSnapshot, wallet exchange.Wallet, asset string) shared.Decimal {
	for _, balance := range snapshot.Balances {
		if balance.Asset == asset && (balance.Wallet == wallet || balance.Wallet == exchange.WalletUnspecified) {
			return balance.Available
		}
	}
	return shared.Zero()
}
