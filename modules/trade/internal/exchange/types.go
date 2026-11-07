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

// MarketOfInstrumentID 由 Trade 内部标的 ID 推导所属市场：加密货币带 -SPOT、-SWAP 后缀，
// 其余（股票）是原始代码。
func MarketOfInstrumentID(instrumentID string) MarketType {
	switch {
	case strings.HasSuffix(instrumentID, "-"+string(MarketTypeSwap)):
		return MarketTypeSwap
	case strings.HasSuffix(instrumentID, "-"+string(MarketTypeSpot)):
		return MarketTypeSpot
	default:
		return MarketTypeStock
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

// AccountMode 是加密货币实盘账户的类型，只由交易所检测写入；模拟盘和股票账户没有。
type AccountMode string

const (
	AccountModeUnspecified            AccountMode = ""
	AccountModeBinancePortfolioMargin AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	AccountModeBinanceStandard        AccountMode = "BINANCE_STANDARD"
	AccountModeOKXUnified             AccountMode = "OKX_UNIFIED"
)

// Valid 允许空值：账户类型在检测之前、以及模拟盘和股票账户上都是空串。
func (m AccountMode) Valid() bool {
	switch m {
	case AccountModeUnspecified, AccountModeBinancePortfolioMargin, AccountModeBinanceStandard, AccountModeOKXUnified:
		return true
	default:
		return false
	}
}

// Wallet 是余额所在的钱包。币安普通账户有现货、合约两个钱包，统一账户有杠杆、统一两个钱包，
// OKX 和模拟盘只有一个统一钱包。
type Wallet string

const (
	WalletUnspecified Wallet = ""
	WalletSpot        Wallet = "SPOT"
	WalletFutures     Wallet = "FUTURES"
	WalletMargin      Wallet = "MARGIN"
	WalletUnified     Wallet = "UNIFIED"
)

func (w Wallet) Valid() bool {
	return w == WalletSpot || w == WalletFutures || w == WalletMargin || w == WalletUnified
}

// WalletFor 返回某个市场的资金所在的钱包：
// 币安普通账户 SPOT→SPOT、SWAP→FUTURES；币安统一账户 SPOT→MARGIN、SWAP→UNIFIED；OKX 统一账户和模拟盘都是 UNIFIED。
// 账户类型还没检测出来（空串）时，OKX 按统一账户，其余按币安普通账户的口径，与适配器当前输出的钱包一致。
func WalletFor(name Exchange, mode AccountMode, market MarketType) Wallet {
	if mode == AccountModeUnspecified && name == ExchangeOKX {
		mode = AccountModeOKXUnified
	}
	switch mode {
	case AccountModeOKXUnified:
		return WalletUnified
	case AccountModeBinancePortfolioMargin:
		if market == MarketTypeSwap {
			return WalletUnified
		}
		return WalletMargin
	default:
		if market == MarketTypeSwap {
			return WalletFutures
		}
		return WalletSpot
	}
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
	// Markets 是账户已开通的市场。同时开通多个市场时，注册表为每个市场各绑一个单市场适配器，
	// 交给 execution.MultiMarket 按标的所属市场路由；单市场适配器拿到的配置里只有一个市场。
	Markets       []MarketType
	ExecutionMode ExecutionMode
	// AccountMode 是检测出来的账户类型（币安统一账户或普通账户、OKX 统一账户）；模拟盘为空。
	AccountMode     AccountMode
	Environment     AccountEnvironment
	SettlementAsset string
	MarginMode      MarginMode
	// CreatedAt 之前的成交不入账，适配器不用为它们多查一次订单。
	CreatedAt time.Time
}

// Market 返回单市场适配器所服务的市场；开通了多个市场（或没有）时返回空串。
func (c AccountConfig) Market() MarketType {
	if len(c.Markets) != 1 {
		return MarketTypeUnspecified
	}
	return c.Markets[0]
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

// MinNotionalAt 返回在某个价格下的最小下单名义金额：交易所给了最小名义金额（币安）就用它；
// 没有（OKX）就用最小下单量折算：minSz × 合约面值 × 价格（现货合约面值为 1；永续要乘 ctVal，漏掉会差几个数量级）。
// 最小下单量和合约面值缺失时返回 0，表示没有可用的下限。
func (i Instrument) MinNotionalAt(price shared.Decimal) shared.Decimal {
	if i.MinNotional.Cmp(shared.Zero()) > 0 {
		return i.MinNotional
	}
	if price.Cmp(shared.Zero()) <= 0 || i.MinExchangeQuantity.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	contractValue := i.ContractValue
	if contractValue.Cmp(shared.Zero()) <= 0 {
		contractValue = shared.MustDecimal("1")
	}
	return i.MinExchangeQuantity.Mul(contractValue).Mul(price)
}

type AssetBalance struct {
	Wallet    Wallet
	Asset     string
	Available shared.Decimal
	Locked    shared.Decimal
	Total     shared.Decimal
	// Borrowed 是借款（统一账户的杠杆钱包），没有借款的钱包为零。
	Borrowed shared.Decimal
}

type AccountSnapshot struct {
	Balances          []AssetBalance
	Equity            shared.Decimal
	AvailableFunds    shared.Decimal
	UsedMargin        shared.Decimal
	MaintenanceMargin shared.Decimal
	UnrealizedPnL     shared.Decimal
	// ExchangeEquity 是交易所给的总权益，只用于展示核对，不参与换算。
	ExchangeEquity    shared.Decimal
	ExchangeUpdatedAt time.Time
	// WalletUpdatedAt 是各钱包余额的读取时间；同时读多个市场的适配器按钱包记录，没有记录的钱包按 ExchangeUpdatedAt 算。
	WalletUpdatedAt map[Wallet]time.Time
	Present         AccountSnapshotPresence
	RequiresSync    bool
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
	Market            MarketType
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
	ClientOrderID string
	// Market 是标的所属的市场：同一个账户的现货、合约代码相同（BTCUSDT），下单必须说明是哪个市场。
	Market         MarketType
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
	Market          MarketType
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
	Market          MarketType
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
