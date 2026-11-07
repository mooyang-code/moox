package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrAccountOwnership   = errors.New("trade order: Exchange account ownership mismatch")
	ErrInstrumentDisabled = errors.New("标的不可交易")
	ErrQuantityRule       = errors.New("数量不符合交易规则")
	ErrNotionalLimit      = errors.New("超过单笔子单金额上限")
	ErrInsufficientFunds  = errors.New("可用资金不足")
	ErrLeverageLimit      = errors.New("杠杆超过上限")
	ErrReduceOnly         = errors.New("只减仓订单无效")
	ErrValidatorConfig    = errors.New("trade order: validator is not configured")
)

type AccountEligibility interface {
	ExecutionEligibility(context.Context, string) (tradingaccount.Account, error)
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
	Account          tradingaccount.Account
	Instrument       exchange.Instrument
	Notional         shared.Decimal
	Leverage         shared.Decimal
	ReservedAsset    string
	ReservedQuantity shared.Decimal
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
	if !account.SupportsMarket(market) {
		return Validation{}, fmt.Errorf("%w: 账户没有开通市场 %s", ErrInstrumentDisabled, market)
	}
	if err := spec.Validate(market, now, v.MaxReferenceAge); err != nil {
		return Validation{}, err
	}
	var instrument exchange.Instrument
	if scoped, ok := v.Instruments.(AccountInstrumentSource); ok {
		instrument, err = scoped.GetInstrumentForAccount(ctx, account.ID, account.Exchange, market, spec.InstrumentID)
	} else {
		instrument, err = v.Instruments.GetInstrument(ctx, account.Exchange, market, spec.InstrumentID)
	}
	if err != nil {
		return Validation{}, err
	}
	if instrument.Status != "TRADING" && instrument.Status != "live" {
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
	) {
		return Validation{}, ErrQuantityRule
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

	result := Validation{
		Account: account, Instrument: instrument, Notional: referenceNotional,
		Leverage: shared.MustDecimal("1"),
	}
	feeRate := v.FeeBufferRate
	if account.ExecutionMode == exchange.ExecutionModePaper && account.Paper != nil {
		// Paper reservations use the configured worst-case fee rather than the
		// process-wide live safety buffer. A first MARKET/LIMIT match may be
		// taker, while a resting GTC LIMIT may later be maker.
		feeRate = maxDecimal(account.Paper.MakerFeeRate, account.Paper.TakerFeeRate)
	}
	if rule := exchange.RuleFor(market); !rule.UsesMargin {
		if spec.Side == exchange.SideBuy {
			result.ReservedAsset = instrument.QuoteAsset
			reserveNotional := referenceNotional
			if account.ExecutionMode == exchange.ExecutionModePaper && spec.Type == exchange.OrderTypeMarket && account.Paper != nil {
				reserveNotional = paperSlippagePrice(referenceNotional, spec.Side, account.Paper.SlippageBPS)
			}
			result.ReservedQuantity = withFeeBuffer(reserveNotional, feeRate)
			if spec.Type == exchange.OrderTypeLimit {
				result.ReservedQuantity = orderNotional
				if account.ExecutionMode == exchange.ExecutionModePaper {
					result.ReservedQuantity = withFeeBuffer(orderNotional, feeRate)
				}
			}
		} else {
			result.ReservedAsset = instrument.BaseAsset
			result.ReservedQuantity = spec.Quantity
		}
		if !capacity && !(deferPaperFunds && account.ExecutionMode == exchange.ExecutionModePaper) &&
			availableFunds(account, market, result.ReservedAsset).Cmp(result.ReservedQuantity) < 0 {
			return Validation{}, ErrInsufficientFunds
		}
	} else {
		leverage, found := account.LeverageSettings[spec.InstrumentID]
		if !found {
			leverage, found = account.LeverageSettings["*"]
		}
		// Paper simulations intentionally keep the lifecycle small: when no
		// per-symbol leverage was configured, use conservative 1x CROSS
		// margin instead of creating an account that can never place an order.
		if !found && account.ExecutionMode == exchange.ExecutionModePaper {
			leverage, found = shared.MustDecimal("1"), true
		}
		if !found || leverage.Cmp(shared.Zero()) <= 0 {
			return Validation{}, fmt.Errorf("%w: missing symbol leverage", ErrLeverageLimit)
		}
		if v.MaxLeverage.Cmp(shared.Zero()) > 0 && leverage.Cmp(v.MaxLeverage) > 0 {
			return Validation{}, ErrLeverageLimit
		}
		result.Leverage = leverage
		result.ReservedAsset = account.SettlementAsset
		reserveNotional := referenceNotional
		if account.ExecutionMode == exchange.ExecutionModePaper && spec.Type == exchange.OrderTypeLimit && spec.LimitPrice != nil {
			reserveNotional = orderNotional
		}
		if account.ExecutionMode == exchange.ExecutionModePaper && spec.Type == exchange.OrderTypeMarket && account.Paper != nil {
			reserveNotional = paperSlippagePrice(referenceNotional, spec.Side, account.Paper.SlippageBPS)
		}
		if spec.ReducePositionOnly {
			if err := v.validateReduceOnly(ctx, spec); err != nil {
				return Validation{}, err
			}
			result.ReduceOnly = true
			if account.ExecutionMode == exchange.ExecutionModePaper {
				result.ReservedQuantity = reserveNotional.Mul(feeRate)
			}
		} else {
			margin := reserveNotional.Div(leverage)
			result.ReservedQuantity = margin
			if account.ExecutionMode == exchange.ExecutionModePaper {
				result.ReservedQuantity = margin.Add(reserveNotional.Mul(feeRate))
			} else {
				result.ReservedQuantity = withFeeBuffer(margin, feeRate)
			}
		}
		if !capacity && !(deferPaperFunds && account.ExecutionMode == exchange.ExecutionModePaper) &&
			availableFunds(account, market, result.ReservedAsset).Cmp(result.ReservedQuantity) < 0 {
			return Validation{}, ErrInsufficientFunds
		}
	}
	return result, nil
}

func maxDecimal(left, right shared.Decimal) shared.Decimal {
	if left.Cmp(right) >= 0 {
		return left
	}
	return right
}

func paperSlippagePrice(notional shared.Decimal, side exchange.Side, slippage shared.Decimal) shared.Decimal {
	if slippage.Cmp(shared.Zero()) <= 0 {
		return notional
	}
	factor := shared.MustDecimal("1").Add(slippage.Div(shared.MustDecimal("10000")))
	if side == exchange.SideSell {
		factor = shared.MustDecimal("1").Sub(slippage.Div(shared.MustDecimal("10000")))
	}
	return notional.Mul(factor)
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

func validQuantity(quantity, step, minimum shared.Decimal) bool {
	if step.Cmp(shared.Zero()) <= 0 || quantity.Cmp(minimum) < 0 {
		return false
	}
	return quantity.Div(step).IsInteger()
}

func withFeeBuffer(amount, rate shared.Decimal) shared.Decimal {
	if rate.Cmp(shared.Zero()) <= 0 {
		return amount
	}
	return amount.Add(amount.Mul(rate))
}

// availableFunds 返回订单所在钱包里某个资产的可用资金。普通账户的现货、合约钱包分开算；
// 统一账户的合约和 OKX、模拟盘共用一个钱包，保证金按账户级可用资金算；只开通合约的账户同样如此。
func availableFunds(account tradingaccount.Account, market exchange.MarketType, asset string) shared.Decimal {
	// 统一账户、OKX、模拟盘各市场共用一个资金池：开通了合约时结算币看账户级可用资金（已扣保证金），其他资产加总各钱包。
	if account.SharesFundsAcrossMarkets() {
		if account.HasLeverageMarket() && asset == account.SettlementAsset {
			return account.Snapshot.AvailableFunds
		}
		return availablePool(account.Snapshot, asset)
	}
	wallet := account.WalletFor(market)
	if exchange.RuleFor(market).UsesMargin && (len(account.Markets) == 1 || wallet == exchange.WalletUnified) {
		return account.Snapshot.AvailableFunds
	}
	return availableBalance(account.Snapshot, wallet, asset)
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
