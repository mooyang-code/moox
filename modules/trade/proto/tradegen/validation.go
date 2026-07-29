package tradepb

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"
)

const (
	maxDecimalLength = 256
	maxPageSize      = 1000
)

var (
	canonicalDecimalPattern         = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	canonicalUnsignedDecimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

func (r *CreateAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !validExchange(r.Exchange) ||
		!validMarketType(r.MarketType) ||
		!validExecutionMode(r.ExecutionMode) {
		return fmt.Errorf("exchange, market_type and execution_mode are required")
	}
	if r.ExecutionMode == ExecutionMode_EXECUTION_MODE_LIVE &&
		strings.TrimSpace(r.CredentialSecretId) == "" {
		return fmt.Errorf("credential_secret_id is required for live execution")
	}
	return nil
}

func (r *UpdateAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	return nil
}

func (r *GetAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	return nil
}

func (r *ListAccountsReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if r.Exchange != nil && !validExchange(r.GetExchange()) {
		return fmt.Errorf("invalid exchange")
	}
	if r.MarketType != nil && !validMarketType(r.GetMarketType()) {
		return fmt.Errorf("invalid market_type")
	}
	if r.ExecutionMode != nil && !validExecutionMode(r.GetExecutionMode()) {
		return fmt.Errorf("invalid execution_mode")
	}
	return validatePage(r.Page)
}

func (r *SetLeverageReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.ExchangeAccountId) == "" ||
		strings.TrimSpace(r.Symbol) == "" {
		return fmt.Errorf("exchange_account_id and symbol are required")
	}
	if !isCanonicalPositiveDecimal(r.Leverage) {
		return fmt.Errorf("leverage must be a canonical positive decimal")
	}
	return nil
}

func (r *PauseAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	if r.Paused && strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("reason is required when pausing an account")
	}
	return nil
}

func (r *SyncAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	return nil
}

func (r *PlaceOrderReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.ExchangeAccountId) == "" ||
		strings.TrimSpace(r.ClientOrderId) == "" ||
		strings.TrimSpace(r.Symbol) == "" {
		return fmt.Errorf("exchange_account_id, client_order_id and symbol are required")
	}
	if !validOrderType(r.OrderType) || !validOrderSide(r.Side) {
		return fmt.Errorf("order_type and side are required")
	}
	if !validTimeInForce(r.TimeInForce) {
		return fmt.Errorf("invalid time_in_force")
	}
	if !validPositionSide(r.PositionSide) {
		return fmt.Errorf("invalid position_side")
	}
	if !isCanonicalPositiveDecimal(r.Quantity) {
		return fmt.Errorf("quantity must be a canonical positive decimal")
	}
	switch r.OrderType {
	case OrderType_ORDER_TYPE_MARKET:
		if r.LimitPrice != nil {
			return fmt.Errorf("limit_price must be empty for market orders")
		}
		if r.TimeInForce != TimeInForce_TIME_IN_FORCE_UNSPECIFIED {
			return fmt.Errorf("time_in_force must be unspecified for market orders")
		}
	case OrderType_ORDER_TYPE_LIMIT:
		if r.LimitPrice == nil || !isCanonicalPositiveDecimal(r.GetLimitPrice()) {
			return fmt.Errorf("limit_price must be a canonical positive decimal for limit orders")
		}
		if r.TimeInForce == TimeInForce_TIME_IN_FORCE_UNSPECIFIED {
			return fmt.Errorf("time_in_force is required for limit orders")
		}
	default:
		return fmt.Errorf("unsupported order_type")
	}
	return nil
}

func validExchange(value Exchange) bool {
	switch value {
	case Exchange_EXCHANGE_BINANCE, Exchange_EXCHANGE_OKX:
		return true
	default:
		return false
	}
}

func validMarketType(value MarketType) bool {
	switch value {
	case MarketType_MARKET_TYPE_SPOT, MarketType_MARKET_TYPE_SWAP:
		return true
	default:
		return false
	}
}

func validExecutionMode(value ExecutionMode) bool {
	switch value {
	case ExecutionMode_EXECUTION_MODE_PAPER, ExecutionMode_EXECUTION_MODE_LIVE:
		return true
	default:
		return false
	}
}

func validOrderType(value OrderType) bool {
	switch value {
	case OrderType_ORDER_TYPE_MARKET, OrderType_ORDER_TYPE_LIMIT:
		return true
	default:
		return false
	}
}

func validTimeInForce(value TimeInForce) bool {
	switch value {
	case TimeInForce_TIME_IN_FORCE_UNSPECIFIED,
		TimeInForce_TIME_IN_FORCE_GTC,
		TimeInForce_TIME_IN_FORCE_IOC,
		TimeInForce_TIME_IN_FORCE_FOK:
		return true
	default:
		return false
	}
}

func validPositionSide(value PositionSide) bool {
	switch value {
	case PositionSide_POSITION_SIDE_UNSPECIFIED, PositionSide_POSITION_SIDE_NET:
		return true
	default:
		return false
	}
}

func validOrderSide(value OrderSide) bool {
	switch value {
	case OrderSide_ORDER_SIDE_BUY, OrderSide_ORDER_SIDE_SELL:
		return true
	default:
		return false
	}
}

func (r *CancelOrderReq) Validate() error {
	if r == nil || strings.TrimSpace(r.OrderId) == "" {
		return fmt.Errorf("order_id is required")
	}
	return nil
}

func (r *CancelAllOrdersReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	return nil
}

func (r *SubmitTargetReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.EventId) == "" ||
		strings.TrimSpace(r.ExecutionId) == "" ||
		strings.TrimSpace(r.StrategyRunId) == "" ||
		strings.TrimSpace(r.ExecutionBindingId) == "" ||
		strings.TrimSpace(r.ExchangeAccountId) == "" ||
		strings.TrimSpace(r.DataRevision) == "" {
		return fmt.Errorf("target identity and data_revision are required")
	}
	if r.EventId != r.ExecutionId {
		return fmt.Errorf("event_id must equal execution_id")
	}
	if r.CommandSequence == 0 || r.CommandSequence > math.MaxInt64 {
		return fmt.Errorf("command_sequence must be between 1 and MaxInt64")
	}
	if r.NotAfter <= 0 || r.NotAfter <= time.Now().UnixMilli() {
		return fmt.Errorf("not_after must be in the future")
	}
	if len(r.Targets) == 0 {
		return fmt.Errorf("targets are required")
	}
	seenSymbols := make(map[string]struct{}, len(r.Targets))
	for i, target := range r.Targets {
		if target == nil {
			return fmt.Errorf("targets[%d] is nil", i)
		}
		if strings.TrimSpace(target.InstrumentId) == "" {
			return fmt.Errorf("targets[%d].instrument_id is required", i)
		}
		symbol := target.Symbol
		if strings.TrimSpace(symbol) == "" {
			return fmt.Errorf("targets[%d].symbol is required", i)
		}
		if _, exists := seenSymbols[symbol]; exists {
			return fmt.Errorf("target symbol %q is duplicated", symbol)
		}
		seenSymbols[symbol] = struct{}{}
		if !isCanonicalDecimal(target.TargetQuantity) {
			return fmt.Errorf("targets[%d].target_quantity must be a canonical decimal", i)
		}
	}
	return nil
}

func (r *GetExecutionReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExecutionId) == "" {
		return fmt.Errorf("execution_id is required")
	}
	return nil
}

func (r *ListExecutionsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	return validatePage(r.Page)
}

func (r *GetOrderReq) Validate() error {
	if r == nil || strings.TrimSpace(r.OrderId) == "" {
		return fmt.Errorf("order_id is required")
	}
	return nil
}

func (r *ListOrdersReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	return validatePage(r.Page)
}

func (r *ListFillsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	return validatePage(r.Page)
}

func (r *ListPositionsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("exchange_account_id is required")
	}
	return nil
}

func validatePage(page *Page) error {
	if page == nil {
		return nil
	}
	if page.Size > maxPageSize {
		return fmt.Errorf("page.size must not exceed %d", maxPageSize)
	}
	if page.Cursor != strings.TrimSpace(page.Cursor) {
		return fmt.Errorf("page.cursor must not have surrounding whitespace")
	}
	return nil
}

func validateTimeRange(startTime, endTime int64) error {
	if startTime < 0 || endTime < 0 {
		return fmt.Errorf("time range must not be negative")
	}
	if startTime > 0 && endTime > 0 && endTime < startTime {
		return fmt.Errorf("end_time must not be before start_time")
	}
	return nil
}

func isCanonicalDecimal(value string) bool {
	if len(value) == 0 ||
		len(value) > maxDecimalLength ||
		!canonicalDecimalPattern.MatchString(value) {
		return false
	}
	_, ok := new(big.Rat).SetString(value)
	return ok
}

func isCanonicalPositiveDecimal(value string) bool {
	if len(value) == 0 ||
		len(value) > maxDecimalLength ||
		!canonicalUnsignedDecimalPattern.MatchString(value) {
		return false
	}
	number, ok := new(big.Rat).SetString(value)
	return ok && number.Sign() > 0
}
