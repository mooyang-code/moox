package rpc

import (
	"errors"
	"strings"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/runtime"
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
	case errors.Is(err, operatorapp.ErrInvalidActionResult):
		code = tradepb.ErrorCode_INNER_ERR
	case errors.Is(err, runtime.ErrSessionConfig):
		// 会话还没建起来：请求没有发到交易所，稍后重试即可；前端据此等几秒重试。
		code = tradepb.ErrorCode_TRADE_SESSION_NOT_READY
	case errors.Is(err, logicalapp.ErrAdoptionRequired):
		// 账户上已有持仓或挂单：前端据此提示勾选“接管已有持仓”再提交。
		code = tradepb.ErrorCode_TRADE_ADOPTION_REQUIRED
	case errors.Is(err, errSpaceRequired):
		code = tradepb.ErrorCode_NO_PERMISSION
	case errors.Is(err, gorm.ErrRecordNotFound),
		errors.Is(err, accountapp.ErrAccountNotFound):
		code = tradepb.ErrorCode_NOT_FOUND
	case errors.Is(err, store.ErrConflict),
		errors.Is(err, accountapp.ErrAccountConflict),
		errors.Is(err, logicalapp.ErrOwnerConflict),
		errors.Is(err, operatorapp.ErrLogicalAccountBusy),
		errors.Is(err, orderapp.ErrIdempotencyConflict):
		code = tradepb.ErrorCode_CONFLICT
	case errors.Is(err, store.ErrInvalidRecord),
		errors.Is(err, operatorapp.ErrInvalidCommand),
		errors.Is(err, orderapp.ErrCrossZero),
		errors.Is(err, tradingaccount.ErrInvalidAccount),
		errors.Is(err, logicalapp.ErrMemberHasExposure),
		errors.Is(err, logicalaccount.ErrMemberElsewhere),
		errors.Is(err, accountapp.ErrAccountInLogicalAccount),
		errors.Is(err, operatorapp.ErrNotInLogicalAccount),
		errors.Is(err, delistapp.ErrInvalidRequest),
		errors.Is(err, delistapp.ErrNoMemberOfExchange),
		errors.Is(err, errLedgerUnsupported),
		errors.Is(err, errLedgerUnavailable),
		errors.Is(err, logicalapp.ErrNotReady),
		errors.Is(err, accountapp.ErrInvalidCredential),
		errors.Is(err, accountapp.ErrInspectionBlocked),
		errors.Is(err, accountapp.ErrReadOnlyCredential),
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

// exchangePBNames 是 proto 枚举与场所名的对照，新增场所时加一行。
var exchangePBNames = map[tradepb.Exchange]exchange.Exchange{
	tradepb.Exchange_EXCHANGE_BINANCE: exchange.ExchangeBinance,
	tradepb.Exchange_EXCHANGE_OKX:     exchange.ExchangeOKX,
}

func exchangeFromPB(value tradepb.Exchange) exchange.Exchange {
	return exchangePBNames[value]
}

func exchangeToPB(value string) tradepb.Exchange {
	for pb, name := range exchangePBNames {
		if string(name) == value {
			return pb
		}
	}
	return tradepb.Exchange_EXCHANGE_UNSPECIFIED
}

func marketFromPB(value tradepb.MarketType) exchange.MarketType {
	switch value {
	case tradepb.MarketType_MARKET_TYPE_SPOT:
		return exchange.MarketTypeSpot
	case tradepb.MarketType_MARKET_TYPE_SWAP:
		return exchange.MarketTypeSwap
	case tradepb.MarketType_MARKET_TYPE_STOCK:
		return exchange.MarketTypeStock
	default:
		return exchange.MarketTypeUnspecified
	}
}

func marketsFromPB(values []tradepb.MarketType) []exchange.MarketType {
	result := make([]exchange.MarketType, len(values))
	for i, value := range values {
		result[i] = marketFromPB(value)
	}
	return result
}

func marketsToPB[T ~string](values []T) []tradepb.MarketType {
	result := make([]tradepb.MarketType, len(values))
	for i, value := range values {
		result[i] = marketToPB(string(value))
	}
	return result
}

func marketToPB(value string) tradepb.MarketType {
	switch exchange.MarketType(value) {
	case exchange.MarketTypeSpot:
		return tradepb.MarketType_MARKET_TYPE_SPOT
	case exchange.MarketTypeSwap:
		return tradepb.MarketType_MARKET_TYPE_SWAP
	case exchange.MarketTypeStock:
		return tradepb.MarketType_MARKET_TYPE_STOCK
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

func environmentFromPB(value tradepb.AccountEnvironment) exchange.AccountEnvironment {
	switch value {
	case tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET:
		return exchange.AccountEnvironmentTestnet
	case tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION:
		return exchange.AccountEnvironmentProduction
	default:
		return exchange.AccountEnvironmentUnspecified
	}
}

func environmentToPB(value string) tradepb.AccountEnvironment {
	switch exchange.AccountEnvironment(value) {
	case exchange.AccountEnvironmentTestnet:
		return tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET
	case exchange.AccountEnvironmentProduction:
		return tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION
	default:
		return tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_UNSPECIFIED
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

func fillPolicyFromPB(value tradepb.FillPolicy) exchange.FillPolicy {
	switch value {
	case tradepb.FillPolicy_FILL_POLICY_GTC:
		return exchange.FillPolicyGTC
	case tradepb.FillPolicy_FILL_POLICY_IOC:
		return exchange.FillPolicyIOC
	case tradepb.FillPolicy_FILL_POLICY_FOK:
		return exchange.FillPolicyFOK
	default:
		return exchange.FillPolicyUnspecified
	}
}

func fillPolicyToPB(value string) tradepb.FillPolicy {
	switch exchange.FillPolicy(value) {
	case exchange.FillPolicyGTC:
		return tradepb.FillPolicy_FILL_POLICY_GTC
	case exchange.FillPolicyIOC:
		return tradepb.FillPolicy_FILL_POLICY_IOC
	case exchange.FillPolicyFOK:
		return tradepb.FillPolicy_FILL_POLICY_FOK
	default:
		return tradepb.FillPolicy_FILL_POLICY_UNSPECIFIED
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

func accountToPB(value store.TradingAccountRecord) *tradepb.TradingAccount {
	if value.TradingAccountID == "" {
		return nil
	}
	balances := make([]*tradepb.AssetBalance, 0, len(value.Snapshot.Balances))
	for _, balance := range value.Snapshot.Balances {
		balances = append(balances, &tradepb.AssetBalance{
			Asset: balance.Asset, Available: balance.Available,
			Locked: balance.Locked, Total: balance.Total,
			Wallet: balance.Wallet, Borrowed: balance.Borrowed,
		})
	}
	account := &tradepb.TradingAccount{
		TradingAccountId: value.TradingAccountID, SpaceId: value.SpaceID,
		Name: value.Name, Exchange: exchangeToPB(value.Exchange),
		Markets:     marketsToPB(value.Markets),
		AccountMode: value.AccountMode, InspectionJson: value.InspectionJSON,
		ExecutionMode:   executionModeToPB(value.ExecutionMode),
		SettlementAsset: value.SettlementAsset, MarginMode: value.MarginMode,
		Status: value.Status, Ready: value.Ready,
		LeverageSettings: mapCopy(value.LeverageSettings),
		Snapshot: &tradepb.TradingAccountSnapshot{
			Balances: balances, Equity: value.Snapshot.Equity,
			AvailableFunds:    value.Snapshot.AvailableFunds,
			UsedMargin:        value.Snapshot.UsedMargin,
			MaintenanceMargin: value.Snapshot.MaintenanceMargin,
			UnrealizedPnl:     value.Snapshot.UnrealizedPnL,
			ExchangeEquity:    value.Snapshot.ExchangeEquity,
			ExchangeUpdatedAt: value.Snapshot.ExchangeUpdatedAt,
		},
		LastSyncAt: value.LastSyncAt, LastReadyAt: value.LastReadyAt,
		LastError: value.LastError, CreatedAt: unixMilli(value.CreatedAt),
		UpdatedAt: unixMilli(value.UpdatedAt),
	}
	if value.ExecutionMode == string(exchange.ExecutionModePaper) {
		config := value.PaperConfig
		if config == nil {
			config = &store.PaperAccountConfigRecord{}
		}
		account.ExecutionConfig = &tradepb.TradingAccount_Paper{Paper: &tradepb.PaperConfig{
			InitialBalance: config.InitialBalance, MakerFeeRate: config.MakerFeeRate,
			TakerFeeRate: config.TakerFeeRate, SlippageBps: config.SlippageBPS,
		}}
	} else {
		account.ExecutionConfig = &tradepb.TradingAccount_Live{Live: &tradepb.LiveConfig{
			Environment: environmentToPB(value.Environment), CredentialSecretId: value.CredentialSecretID,
		}}
	}
	return account
}

func logicalAccountToPB(
	value store.LogicalAccountRecord,
	members []store.LogicalAccountMemberRecord,
	markets []string,
	readiness logicalapp.Readiness,
) *tradepb.LogicalAccount {
	if value.LogicalAccountID == "" {
		return nil
	}
	pbMembers := make([]*tradepb.LogicalAccountMember, 0, len(members))
	for _, member := range members {
		pbMembers = append(pbMembers, &tradepb.LogicalAccountMember{
			TradingAccountId: member.TradingAccountID,
			Enabled:          member.Enabled,
			Priority:         int32(member.Priority),
		})
	}
	orderSettings, orderSettingsError := orderSettingsToPB(value.OrderSettingsJSON)
	fundSettings, fundSettingsError := fundSettingsToPB(value.FundSettingsJSON)
	return &tradepb.LogicalAccount{
		LogicalAccountId: value.LogicalAccountID,
		SpaceId:          value.SpaceID,
		Name:             value.Name,
		OwnerGeneration:  value.OwnerGeneration,
		OwnerInstanceId:  value.OwnerInstanceID,
		OwnerSessionId:   value.OwnerSessionID,
		AuthFence:        value.AuthFence,
		ExecutionMode:    executionModeToPB(value.ExecutionMode),
		ControlMode:      controlModeToPB(value.ControlMode),
		Markets:          marketsToPB(markets),
		SettlementAsset:  value.SettlementAsset,
		AutomationState:  value.AutomationState,
		PauseReason:      value.PauseReason,
		Members:          pbMembers,
		Ready:            readiness.Ready,
		ReadinessReasons: append([]string(nil), readiness.Reasons...),
		CreatedAt:        unixMilli(value.CreatedAt),
		UpdatedAt:        unixMilli(value.UpdatedAt),
		OrderSettings:    orderSettings, OrderSettingsError: orderSettingsError,
		FundSettings: fundSettings, FundSettingsError: fundSettingsError,
	}
}

func orderToPB(value store.OrderRecord) *tradepb.Order {
	if value.OrderID == "" {
		return nil
	}
	return &tradepb.Order{
		OrderId: value.OrderID, TradingAccountId: value.TradingAccountID,
		ClientOrderId: value.ClientOrderID, ExchangeOrderId: value.ExchangeOrderID,
		Exchange: exchangeToPB(value.Exchange), MarketType: marketToPB(value.MarketType),
		InstrumentId: value.InstrumentID, ExchangeSymbol: value.ExchangeSymbol,
		OrderType:  orderTypeToPB(value.OrderType),
		FillPolicy: fillPolicyToPB(value.TimeInForce), Side: sideToPB(value.Side),
		PositionSide: positionSideToPB(value.PositionSide), Quantity: value.Quantity,
		LimitPrice: value.LimitPrice, ReferencePrice: value.ReferencePrice,
		ReferencePriceAt:   value.ReferencePriceAt,
		ReducePositionOnly: value.ReduceOnly,
		OwnerType:          value.OwnerType, OwnerId: value.OwnerID,
		LogicalAccountId: value.LogicalAccountID, RunnerId: value.RunnerID,
		State: value.State, Attempt: int32(value.Attempt), FilledQuantity: value.FilledQuantity,
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
		TradingAccountId: value.TradingAccountID,
		Exchange:         exchangeToPB(value.Exchange), MarketType: marketToPB(value.MarketType),
		InstrumentId: value.InstrumentID, ExchangeSymbol: value.ExchangeSymbol, Side: sideToPB(value.Side),
		PositionSide: positionSideToPB(value.PositionSide),
		Price:        value.Price, Quantity: value.Quantity, Fee: value.Fee,
		FeeAsset: value.FeeAsset, SettlementAsset: value.SettlementAsset,
		RealizedPnl: value.RealizedPnL, Role: value.Role,
		TradedAt: value.TradedAt, CreatedAt: unixMilli(value.CreatedAt),
	}
}

func positionToPB(value store.PositionRecord) *tradepb.Position {
	return &tradepb.Position{
		MarketType:       marketToPB(value.MarketType),
		TradingAccountId: value.TradingAccountID, InstrumentId: value.InstrumentID, ExchangeSymbol: value.ExchangeSymbol,
		PositionSide:   positionSideToPB(value.PositionSide),
		SignedQuantity: value.SignedQuantity, EntryPrice: value.EntryPrice,
		MarkPrice: value.MarkPrice, Leverage: value.Leverage,
		MarginMode: value.MarginMode, UsedMargin: value.UsedMargin,
		LiquidationPrice: value.LiquidationPrice,
		UnrealizedPnl:    value.UnrealizedPnL, RealizedPnl: value.RealizedPnL,
		ExchangeUpdatedAt: value.ExchangeUpdatedAt, UpdatedAt: unixMilli(value.UpdatedAt),
	}
}

func logicalAccountTargetToPB(
	value store.LogicalAccountTargetRecord,
) *tradepb.LogicalAccountTarget {
	if value.LogicalAccountID == "" {
		return nil
	}
	targets := make([]*tradepb.InstrumentTarget, 0, len(value.Targets))
	for _, target := range value.Targets {
		targets = append(targets, &tradepb.InstrumentTarget{
			InstrumentId: target.InstrumentID,
			Quantity:     target.Quantity,
			Hold:         target.Hold,
		})
	}
	blocked := make([]*tradepb.BlockedTarget, 0, len(value.BlockedTargets))
	for _, target := range value.BlockedTargets {
		blocked = append(blocked, &tradepb.BlockedTarget{
			InstrumentId: target.InstrumentID,
			Quantity:     target.Quantity,
			Reason:       target.Reason,
			Deferred:     target.Deferred,
		})
	}
	return &tradepb.LogicalAccountTarget{
		TargetId: value.TargetID, LogicalAccountId: value.LogicalAccountID,
		RunnerId: value.RunnerID, CommandSequence: value.CommandSequence,
		InstanceId: value.InstanceID, SessionId: value.SessionID, StrategyId: value.StrategyID,
		BarEndTime: value.BarEndTime, EffectiveAt: value.EffectiveAt, ValidUntil: value.ValidUntil,
		Targets: targets, Status: value.Status, BlockedTargets: blocked,
		LastError: value.LastError, AcceptedAt: value.AcceptedAt,
		UpdatedAt: unixMilli(value.UpdatedAt),
	}
}

func operatorActionToPB(value store.OperatorActionRecord) *tradepb.OperatorAction {
	if value.ActionID == "" {
		return nil
	}
	var result string
	if value.ResultJSON != nil {
		result = *value.ResultJSON
	}
	return &tradepb.OperatorAction{
		ActionId: value.ActionID, LogicalAccountId: value.LogicalAccountID,
		ActionType: value.ActionType, Reason: value.Reason, Status: value.Status,
		ResultJson: result, LastError: value.LastError,
		CreatedAt: unixMilli(value.CreatedAt), UpdatedAt: unixMilli(value.UpdatedAt),
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
