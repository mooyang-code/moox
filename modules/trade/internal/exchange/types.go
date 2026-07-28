package exchange

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type Exchange string

const (
	ExchangeUnspecified Exchange = ""
	ExchangeBinance     Exchange = "BINANCE"
	ExchangeOKX         Exchange = "OKX"
)

func (e Exchange) Valid() bool {
	return e == ExchangeBinance || e == ExchangeOKX
}

type MarketType string

const (
	MarketTypeUnspecified MarketType = ""
	MarketTypeSpot        MarketType = "SPOT"
	MarketTypeSwap        MarketType = "SWAP"
)

func (m MarketType) Valid() bool {
	return m == MarketTypeSpot || m == MarketTypeSwap
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
	OrderStatusPending           OrderStatus = "PENDING"
	OrderStatusSubmitting        OrderStatus = "SUBMITTING"
	OrderStatusSubmitUnknown     OrderStatus = "SUBMIT_UNKNOWN"
	OrderStatusOpen              OrderStatus = "OPEN"
	OrderStatusPartiallyFilled   OrderStatus = "PARTIALLY_FILLED"
	OrderStatusCanceling         OrderStatus = "CANCELING"
	OrderStatusCancelUnknown     OrderStatus = "CANCEL_UNKNOWN"
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
}

type AccountConfig struct {
	ExchangeAccountID string
	Exchange          Exchange
	MarketType        MarketType
	ExecutionMode     ExecutionMode
	SettlementAsset   string
	MarginMode        MarginMode
}

type Instrument struct {
	Exchange             Exchange
	MarketType           MarketType
	Symbol               string
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
	ExchangeAccountID string
	Symbol            string
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
	Present           PositionPresence
	RequiresSync      bool
}

type PositionPresence struct {
	SignedQuantity   bool
	EntryPrice       bool
	MarkPrice        bool
	Leverage         bool
	MarginMode       bool
	UsedMargin       bool
	LiquidationPrice bool
	UnrealizedPnL    bool
	RealizedPnL      bool
}

type OrderRequest struct {
	ClientOrderID string
	Symbol        string
	OrderType     OrderType
	TimeInForce   TimeInForce
	Side          Side
	PositionSide  PositionSide
	Quantity      shared.Decimal
	LimitPrice    *shared.Decimal
	ReduceOnly    bool
}

type Order struct {
	ExchangeOrderID string
	ClientOrderID   string
	Symbol          string
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
	Symbol          string
	Side            Side
	PositionSide    PositionSide
	Quantity        shared.Decimal
	Price           shared.Decimal
	Fee             shared.Decimal
	FeeAsset        string
	RealizedPnL     shared.Decimal
	SettlementAsset string
	LiquidityRole   string
	TradedAt        time.Time
}

type EventHandler interface {
	OnOrder(context.Context, Order) error
	OnFill(context.Context, Fill) error
	OnPosition(context.Context, Position) error
	OnAccountSnapshot(context.Context, AccountSnapshot) error
}

type PrivateReadyHandler interface {
	OnPrivateReady()
}

func NotifyPrivateReady(handler EventHandler) {
	if ready, ok := handler.(PrivateReadyHandler); ok {
		ready.OnPrivateReady()
	}
}
