package exchange

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// CanonicalInstrumentID identifies the same economic instrument across
// Exchange-native symbol formats.
func CanonicalInstrumentID(
	baseAsset string,
	quoteAsset string,
	market MarketType,
) (string, error) {
	base := strings.ToUpper(strings.TrimSpace(baseAsset))
	quote := strings.ToUpper(strings.TrimSpace(quoteAsset))
	if base == "" || quote == "" || !market.Valid() {
		return "", fmt.Errorf("trade Exchange: invalid canonical instrument identity")
	}
	if RuleFor(market).RawInstrumentID {
		return base, nil
	}
	return base + "-" + quote + "-" + string(market), nil
}

// ErrInvalidInstrumentID 表示上游传入的标的 ID 无法识别，或与账户市场不一致。
var ErrInvalidInstrumentID = errors.New("trade Exchange: invalid instrument ID")

// NormalizeInstrumentID 把上游传入的标的 ID 标准化成 Trade 内部 ID。
// 采集和策略用不带市场后缀的 BTC-USDT，Trade 内部用 BTC-USDT-SPOT、BTC-USDT-SWAP。
func NormalizeInstrumentID(raw string, market MarketType) (string, error) {
	value := strings.ToUpper(strings.TrimSpace(raw))
	parts := strings.Split(value, "-")
	switch {
	case !market.Valid():
		return "", fmt.Errorf("%w: unsupported market %q", ErrInvalidInstrumentID, market)
	case RuleFor(market).RawInstrumentID:
		if value == "" || strings.ContainsAny(value, " \t") {
			return "", fmt.Errorf("%w: %q for market %s", ErrInvalidInstrumentID, raw, market)
		}
		return value, nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return CanonicalInstrumentID(parts[0], parts[1], market)
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] == string(market):
		return value, nil
	default:
		return "", fmt.Errorf("%w: %q for market %s", ErrInvalidInstrumentID, raw, market)
	}
}

// Exchange 指交易场所，可以是交易所，也可以是券商。
type Exchange string

const (
	ExchangeUnspecified Exchange = ""
	ExchangeBinance     Exchange = "BINANCE"
	ExchangeOKX         Exchange = "OKX"
)

// Valid 只检查非空。是否支持由 execution.Registry 判断，新增场所不用改这里。
func (e Exchange) Valid() bool {
	return e != ExchangeUnspecified
}

type MarketType string

const (
	MarketTypeUnspecified MarketType = ""
	MarketTypeSpot        MarketType = "SPOT"
	MarketTypeSwap        MarketType = "SWAP"
	MarketTypeStock       MarketType = "STOCK"
)

func (m MarketType) Valid() bool {
	_, ok := marketRules[m]
	return ok
}

type ExecutionMode string

const (
	ExecutionModeUnspecified ExecutionMode = ""
	ExecutionModePaper       ExecutionMode = "PAPER"
	ExecutionModeLive        ExecutionMode = "LIVE"
)

func (m ExecutionMode) Valid() bool {
	return m == ExecutionModePaper || m == ExecutionModeLive
}

type AccountEnvironment string

const (
	AccountEnvironmentUnspecified AccountEnvironment = ""
	AccountEnvironmentPaper       AccountEnvironment = "PAPER"
	AccountEnvironmentTestnet     AccountEnvironment = "TESTNET"
	AccountEnvironmentProduction  AccountEnvironment = "PRODUCTION"
)

func (e AccountEnvironment) Valid() bool {
	return e == AccountEnvironmentPaper ||
		e == AccountEnvironmentTestnet ||
		e == AccountEnvironmentProduction
}

func (e AccountEnvironment) ValidLive() bool {
	return e == AccountEnvironmentTestnet || e == AccountEnvironmentProduction
}

type MarginMode string

const (
	MarginModeUnspecified MarginMode = ""
	MarginModeCross       MarginMode = "CROSS"
)

type OrderType string

const (
	OrderTypeUnspecified OrderType = ""
	OrderTypeMarket      OrderType = "MARKET"
	OrderTypeLimit       OrderType = "LIMIT"
)

type TimeInForce string

const (
	TimeInForceUnspecified TimeInForce = ""
	TimeInForceGTC         TimeInForce = "GTC"
	TimeInForceIOC         TimeInForce = "IOC"
	TimeInForceFOK         TimeInForce = "FOK"
)

func (t TimeInForce) ValidForLimit() bool {
	return t == TimeInForceGTC || t == TimeInForceIOC || t == TimeInForceFOK
}

// FillPolicy is the domain-level lifetime policy for a LIMIT order. Adapters
// translate it to their Exchange-native time-in-force or order-type field.
type FillPolicy string

const (
	FillPolicyUnspecified FillPolicy = ""
	FillPolicyGTC         FillPolicy = "GTC"
	FillPolicyIOC         FillPolicy = "IOC"
	FillPolicyFOK         FillPolicy = "FOK"
)

func (p FillPolicy) ValidForLimit() bool {
	return p == FillPolicyGTC || p == FillPolicyIOC || p == FillPolicyFOK
}

type Side string

const (
	SideUnspecified Side = ""
	SideBuy         Side = "BUY"
	SideSell        Side = "SELL"
)

func (s Side) Valid() bool {
	return s == SideBuy || s == SideSell
}

type PositionSide string

const (
	PositionSideUnspecified PositionSide = ""
	PositionSideNet         PositionSide = "NET"
)

type AccountStatus string

const (
	AccountStatusEnabled  AccountStatus = "ENABLED"
	AccountStatusDisabled AccountStatus = "DISABLED"
	AccountStatusError    AccountStatus = "ERROR"
)

type OrderStatus string

const (
	OrderStatusSubmitUnknown     OrderStatus = "SUBMIT_UNKNOWN"
	OrderStatusOpen              OrderStatus = "OPEN"
	OrderStatusPartiallyFilled   OrderStatus = "PARTIALLY_FILLED"
	OrderStatusFilled            OrderStatus = "FILLED"
	OrderStatusCanceled          OrderStatus = "CANCELED"
	OrderStatusPartiallyCanceled OrderStatus = "PARTIALLY_CANCELED"
	OrderStatusRejected          OrderStatus = "REJECTED"
	OrderStatusExpired           OrderStatus = "EXPIRED"
)

type Credential struct {
	APIKey     string
	APISecret  string
	Passphrase string
	// Extra 放场所特有的配置，比如券商账号和网关地址。
	Extra map[string]string
}

type AccountConfig struct {
	TradingAccountID string
	Exchange         Exchange
	MarketType       MarketType
	ExecutionMode    ExecutionMode
	Environment      AccountEnvironment
	SettlementAsset  string
	MarginMode       MarginMode
	// CreatedAt 之前的成交不入账，适配器不用为它们多查一次订单。
	CreatedAt time.Time
}

type Instrument struct {
	Exchange             Exchange
	MarketType           MarketType
	ExchangeSymbol       string
	InstrumentID         string
	BaseAsset            string
	QuoteAsset           string
	SettlementAsset      string
	Linear               bool
	ContractValue        shared.Decimal
	ContractValueAsset   string
	ExchangeQuantityStep shared.Decimal
	MinExchangeQuantity  shared.Decimal
	PriceTick            shared.Decimal
	MinNotional          shared.Decimal
	Status               string
	ExchangeUpdatedAt    time.Time
}

type AssetBalance struct {
	Asset     string
	Available shared.Decimal
	Locked    shared.Decimal
	Total     shared.Decimal
}

type AccountSnapshot struct {
	Balances          []AssetBalance
	Equity            shared.Decimal
	AvailableFunds    shared.Decimal
	UsedMargin        shared.Decimal
	MaintenanceMargin shared.Decimal
	UnrealizedPnL     shared.Decimal
	ExchangeUpdatedAt time.Time
	Present           AccountSnapshotPresence
	RequiresSync      bool
}

type AccountSnapshotPresence struct {
	Balances          bool
	Equity            bool
	AvailableFunds    bool
	UsedMargin        bool
	MaintenanceMargin bool
	UnrealizedPnL     bool
}

type Position struct {
	TradingAccountID  string
	InstrumentID      string
	ExchangeSymbol    string
	PositionSide      PositionSide
	SignedQuantity    shared.Decimal
	EntryPrice        shared.Decimal
	MarkPrice         shared.Decimal
	Leverage          shared.Decimal
	MarginMode        MarginMode
	UsedMargin        shared.Decimal
	LiquidationPrice  shared.Decimal
	UnrealizedPnL     shared.Decimal
	RealizedPnL       shared.Decimal
	ExchangeUpdatedAt time.Time
}

type OrderRequest struct {
	ClientOrderID  string
	ExchangeSymbol string
	OrderType      OrderType
	FillPolicy     FillPolicy
	Side           Side
	PositionSide   PositionSide
	Quantity       shared.Decimal
	LimitPrice     *shared.Decimal
	ReferencePrice shared.Decimal
	ReduceOnly     bool
}

func (r OrderRequest) EffectiveFillPolicy() FillPolicy {
	return r.FillPolicy
}

func (r OrderRequest) NativeTimeInForce() TimeInForce {
	return TimeInForce(r.EffectiveFillPolicy())
}

type Order struct {
	ExchangeOrderID string
	ClientOrderID   string
	ExchangeSymbol  string
	OrderType       OrderType
	TimeInForce     TimeInForce
	Side            Side
	PositionSide    PositionSide
	Quantity        shared.Decimal
	LimitPrice      *shared.Decimal
	FilledQuantity  shared.Decimal
	AveragePrice    shared.Decimal
	ReduceOnly      bool
	Status          OrderStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Fill struct {
	ExchangeTradeID string
	ExchangeOrderID string
	ClientOrderID   string
	ExchangeSymbol  string
	Side            Side
	PositionSide    PositionSide
	Quantity        shared.Decimal
	Price           shared.Decimal
	// Fee is a signed cost: positive charges funds, negative credits a rebate.
	Fee             shared.Decimal
	FeeAsset        string
	RealizedPnL     shared.Decimal
	SettlementAsset string
	LiquidityRole   string
	TradedAt        time.Time
}
