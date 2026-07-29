package rpc

import (
	"errors"
	"strings"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"gorm.io/gorm"
)

func retInfo(code tradepb.ErrorCode, message string) *tradepb.RetInfo {
	return &tradepb.RetInfo{Code: code, Msg: message}
}

func success() *tradepb.RetInfo {
	return retInfo(tradepb.ErrorCode_SUCCESS, "")
}

func errorInfo(err error) *tradepb.RetInfo {
	if err == nil {
		return success()
	}
	code := tradepb.ErrorCode_INNER_ERR
	switch {
	case errors.Is(err, errSpaceRequired):
		code = tradepb.ErrorCode_NO_PERMISSION
	case errors.Is(err, gorm.ErrRecordNotFound),
		errors.Is(err, accountapp.ErrAccountNotFound):
		code = tradepb.ErrorCode_NOT_FOUND
	case errors.Is(err, store.ErrConflict),
		errors.Is(err, accountapp.ErrAccountConflict),
		errors.Is(err, orderapp.ErrIdempotencyConflict):
		code = tradepb.ErrorCode_CONFLICT
	case errors.Is(err, store.ErrInvalidRecord),
		errors.Is(err, exchangeaccount.ErrInvalidAccount),
		errors.Is(err, accountapp.ErrInvalidCredential),
		errors.Is(err, orderdomain.ErrInvalidOrder),
		errors.Is(err, orderdomain.ErrInvalidSpec),
		errors.Is(err, orderapp.ErrAccountOwnership),
		errors.Is(err, orderapp.ErrInstrumentDisabled),
		errors.Is(err, orderapp.ErrQuantityRule),
		errors.Is(err, orderapp.ErrNotionalLimit),
		errors.Is(err, orderapp.ErrInsufficientFunds),
		errors.Is(err, orderapp.ErrLeverageLimit),
		errors.Is(err, orderapp.ErrReduceOnly):
		code = tradepb.ErrorCode_INVALID_PARAM
	}
	return retInfo(code, err.Error())
}

type pageSpec struct {
	number int
	size   int
	offset int
}

func pageFromPB(value *tradepb.Page) pageSpec {
	number, size := 1, 50
	if value != nil {
		if value.GetPage() > 0 {
			number = int(value.GetPage())
		}
		if value.GetSize() > 0 {
			size = int(value.GetSize())
		}
	}
	if size > 1000 {
		size = 1000
	}
	return pageSpec{number: number, size: size, offset: (number - 1) * size}
}

func pageResult(page pageSpec, total int64) *tradepb.PageResult {
	return &tradepb.PageResult{
		Page: uint32(page.number), Size: uint32(page.size), Total: uint32(total),
		HasMore: int64(page.offset+page.size) < total,
	}
}

func exchangeFromPB(value tradepb.Exchange) exchange.Exchange {
	switch value {
	case tradepb.Exchange_EXCHANGE_BINANCE:
		return exchange.ExchangeBinance
	case tradepb.Exchange_EXCHANGE_OKX:
		return exchange.ExchangeOKX
	default:
		return exchange.ExchangeUnspecified
	}
}

func exchangeToPB(value string) tradepb.Exchange {
	switch exchange.Exchange(value) {
	case exchange.ExchangeBinance:
		return tradepb.Exchange_EXCHANGE_BINANCE
	case exchange.ExchangeOKX:
		return tradepb.Exchange_EXCHANGE_OKX
	default:
		return tradepb.Exchange_EXCHANGE_UNSPECIFIED
	}
}

func marketFromPB(value tradepb.MarketType) exchange.MarketType {
	switch value {
	case tradepb.MarketType_MARKET_TYPE_SPOT:
		return exchange.MarketTypeSpot
	case tradepb.MarketType_MARKET_TYPE_SWAP:
		return exchange.MarketTypeSwap
	default:
		return exchange.MarketTypeUnspecified
	}
}

func marketToPB(value string) tradepb.MarketType {
	switch exchange.MarketType(value) {
	case exchange.MarketTypeSpot:
		return tradepb.MarketType_MARKET_TYPE_SPOT
	case exchange.MarketTypeSwap:
		return tradepb.MarketType_MARKET_TYPE_SWAP
	default:
		return tradepb.MarketType_MARKET_TYPE_UNSPECIFIED
	}
}

func executionModeFromPB(value tradepb.ExecutionMode) exchange.ExecutionMode {
	switch value {
	case tradepb.ExecutionMode_EXECUTION_MODE_PAPER:
		return exchange.ExecutionModePaper
	case tradepb.ExecutionMode_EXECUTION_MODE_LIVE:
		return exchange.ExecutionModeLive
	default:
		return exchange.ExecutionModeUnspecified
	}
}

func executionModeToPB(value string) tradepb.ExecutionMode {
	switch exchange.ExecutionMode(value) {
	case exchange.ExecutionModePaper:
		return tradepb.ExecutionMode_EXECUTION_MODE_PAPER
	case exchange.ExecutionModeLive:
		return tradepb.ExecutionMode_EXECUTION_MODE_LIVE
	default:
		return tradepb.ExecutionMode_EXECUTION_MODE_UNSPECIFIED
	}
}

func orderTypeFromPB(value tradepb.OrderType) exchange.OrderType {
	switch value {
	case tradepb.OrderType_ORDER_TYPE_MARKET:
		return exchange.OrderTypeMarket
	case tradepb.OrderType_ORDER_TYPE_LIMIT:
		return exchange.OrderTypeLimit
	default:
		return exchange.OrderTypeUnspecified
	}
}

func orderTypeToPB(value string) tradepb.OrderType {
	switch exchange.OrderType(value) {
	case exchange.OrderTypeMarket:
		return tradepb.OrderType_ORDER_TYPE_MARKET
	case exchange.OrderTypeLimit:
		return tradepb.OrderType_ORDER_TYPE_LIMIT
	default:
		return tradepb.OrderType_ORDER_TYPE_UNSPECIFIED
	}
}

func timeInForceFromPB(value tradepb.TimeInForce) exchange.TimeInForce {
	switch value {
	case tradepb.TimeInForce_TIME_IN_FORCE_GTC:
		return exchange.TimeInForceGTC
	case tradepb.TimeInForce_TIME_IN_FORCE_IOC:
		return exchange.TimeInForceIOC
	case tradepb.TimeInForce_TIME_IN_FORCE_FOK:
		return exchange.TimeInForceFOK
	default:
		return exchange.TimeInForceUnspecified
	}
}

func timeInForceToPB(value string) tradepb.TimeInForce {
	switch exchange.TimeInForce(value) {
	case exchange.TimeInForceGTC:
		return tradepb.TimeInForce_TIME_IN_FORCE_GTC
	case exchange.TimeInForceIOC:
		return tradepb.TimeInForce_TIME_IN_FORCE_IOC
	case exchange.TimeInForceFOK:
		return tradepb.TimeInForce_TIME_IN_FORCE_FOK
	default:
		return tradepb.TimeInForce_TIME_IN_FORCE_UNSPECIFIED
	}
}

func sideFromPB(value tradepb.OrderSide) exchange.Side {
	switch value {
	case tradepb.OrderSide_ORDER_SIDE_BUY:
		return exchange.SideBuy
	case tradepb.OrderSide_ORDER_SIDE_SELL:
		return exchange.SideSell
	default:
		return exchange.SideUnspecified
	}
}

func sideToPB(value string) tradepb.OrderSide {
	switch exchange.Side(value) {
	case exchange.SideBuy:
		return tradepb.OrderSide_ORDER_SIDE_BUY
	case exchange.SideSell:
		return tradepb.OrderSide_ORDER_SIDE_SELL
	default:
		return tradepb.OrderSide_ORDER_SIDE_UNSPECIFIED
	}
}

func positionSideFromPB(value tradepb.PositionSide) exchange.PositionSide {
	if value == tradepb.PositionSide_POSITION_SIDE_NET {
		return exchange.PositionSideNet
	}
	return exchange.PositionSideUnspecified
}

func positionSideToPB(value string) tradepb.PositionSide {
	if exchange.PositionSide(value) == exchange.PositionSideNet {
		return tradepb.PositionSide_POSITION_SIDE_NET
	}
	return tradepb.PositionSide_POSITION_SIDE_UNSPECIFIED
}

func accountToPB(value store.ExchangeAccountRecord) *tradepb.ExchangeAccount {
	if value.ExchangeAccountID == "" {
		return nil
	}
	balances := make([]*tradepb.AssetBalance, 0, len(value.Snapshot.Balances))
	for _, balance := range value.Snapshot.Balances {
		balances = append(balances, &tradepb.AssetBalance{
			Asset: balance.Asset, Available: balance.Available,
			Locked: balance.Locked, Total: balance.Total,
		})
	}
	return &tradepb.ExchangeAccount{
		ExchangeAccountId: value.ExchangeAccountID, SpaceId: value.SpaceID,
		Name: value.Name, Exchange: exchangeToPB(value.Exchange),
		MarketType:         marketToPB(value.MarketType),
		ExecutionMode:      executionModeToPB(value.ExecutionMode),
		CredentialSecretId: value.CredentialSecretID,
		SettlementAsset:    value.SettlementAsset, MarginMode: value.MarginMode,
		Status: value.Status, Paused: value.Paused, PauseReason: value.PauseReason,
		Ready: value.Ready, SyncSymbols: append([]string(nil), value.SyncSymbols...),
		LeverageSettings: mapCopy(value.LeverageSettings),
		Snapshot: &tradepb.ExchangeAccountSnapshot{
			Balances: balances, Equity: value.Snapshot.Equity,
			AvailableFunds:    value.Snapshot.AvailableFunds,
			UsedMargin:        value.Snapshot.UsedMargin,
			MaintenanceMargin: value.Snapshot.MaintenanceMargin,
			UnrealizedPnl:     value.Snapshot.UnrealizedPnL,
			ExchangeUpdatedAt: value.Snapshot.ExchangeUpdatedAt,
		},
		LastSyncAt: value.LastSyncAt, LastReadyAt: value.LastReadyAt,
		LastError: value.LastError, CreatedAt: unixMilli(value.CreatedAt),
		UpdatedAt: unixMilli(value.UpdatedAt),
	}
}

func orderToPB(value store.OrderRecord) *tradepb.Order {
	if value.OrderID == "" {
		return nil
	}
	return &tradepb.Order{
		OrderId: value.OrderID, ExchangeAccountId: value.ExchangeAccountID,
		ClientOrderId: value.ClientOrderID, ExchangeOrderId: value.ExchangeOrderID,
		Exchange: exchangeToPB(value.Exchange), MarketType: marketToPB(value.MarketType),
		Symbol: value.Symbol, OrderType: orderTypeToPB(value.OrderType),
		TimeInForce: timeInForceToPB(value.TimeInForce), Side: sideToPB(value.Side),
		PositionSide: positionSideToPB(value.PositionSide), Quantity: value.Quantity,
		LimitPrice: value.LimitPrice, ReferencePrice: value.ReferencePrice,
		ReferencePriceAt: value.ReferencePriceAt, ReduceOnly: value.ReduceOnly,
		Source: value.Source, StrategyExecutionId: value.StrategyExecutionID,
		State: value.State, FilledQuantity: value.FilledQuantity,
		AveragePrice: value.AveragePrice, ReservedAsset: value.ReservedAsset,
		ReservedQuantity:          value.ReservedQuantity,
		RemainingReservedQuantity: value.RemainingReservedQuantity,
		RejectReason:              value.RejectReason, Version: value.Version,
		SubmittedAt: value.SubmittedAt, FinishedAt: value.FinishedAt,
		CreatedAt: unixMilli(value.CreatedAt), UpdatedAt: unixMilli(value.UpdatedAt),
	}
}

func fillToPB(value store.FillRecord) *tradepb.Fill {
	return &tradepb.Fill{
		FillId: value.FillID, ExchangeTradeId: value.ExchangeTradeID,
		OrderId: value.OrderID, ExchangeOrderId: value.ExchangeOrderID,
		ExchangeAccountId: value.ExchangeAccountID,
		Exchange:          exchangeToPB(value.Exchange), MarketType: marketToPB(value.MarketType),
		Symbol: value.Symbol, Side: sideToPB(value.Side),
		PositionSide: positionSideToPB(value.PositionSide),
		Price:        value.Price, Quantity: value.Quantity, Fee: value.Fee,
		FeeAsset: value.FeeAsset, SettlementAsset: value.SettlementAsset,
		RealizedPnl: value.RealizedPnL, Role: value.Role,
		TradedAt: value.TradedAt, CreatedAt: unixMilli(value.CreatedAt),
	}
}

func positionToPB(value store.PositionRecord) *tradepb.Position {
	return &tradepb.Position{
		ExchangeAccountId: value.ExchangeAccountID, Symbol: value.Symbol,
		PositionSide:   positionSideToPB(value.PositionSide),
		SignedQuantity: value.SignedQuantity, EntryPrice: value.EntryPrice,
		MarkPrice: value.MarkPrice, Leverage: value.Leverage,
		MarginMode: value.MarginMode, UsedMargin: value.UsedMargin,
		LiquidationPrice: value.LiquidationPrice,
		UnrealizedPnl:    value.UnrealizedPnL, RealizedPnl: value.RealizedPnL,
		ExchangeUpdatedAt: value.ExchangeUpdatedAt, UpdatedAt: unixMilli(value.UpdatedAt),
	}
}

func targetToPB(value store.TargetExecutionRecord) *tradepb.TargetExecution {
	targets := make([]*tradepb.TargetPosition, 0, len(value.Targets))
	for _, target := range value.Targets {
		targets = append(targets, &tradepb.TargetPosition{
			InstrumentId: target.InstrumentID, Symbol: target.Symbol,
			TargetQuantity: target.TargetQuantity,
		})
	}
	return &tradepb.TargetExecution{
		ExecutionId: value.ExecutionID, EventId: value.EventID,
		StrategyRunId:      value.StrategyRunID,
		ExecutionBindingId: value.ExecutionBindingID,
		ExchangeAccountId:  value.ExchangeAccountID,
		CommandSequence:    value.CommandSequence, NotAfter: value.NotAfter,
		DataRevision: value.DataRevision, Targets: targets, Status: value.Status,
		Progress: value.Progress, ResidualQuantity: value.ResidualQuantity,
		LastError: value.LastError, CreatedAt: unixMilli(value.CreatedAt),
		UpdatedAt: unixMilli(value.UpdatedAt),
	}
}

func unixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func mapCopy(values map[string]string) map[string]string {
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func normalized(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}
