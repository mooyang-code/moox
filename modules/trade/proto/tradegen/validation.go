package tradepb

import (
	"fmt"
	"strings"
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

func (r *ListAccountsReq) Validate() error {
	if r == nil {
		return nil
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
	if strings.TrimSpace(r.Quantity) == "" {
		return fmt.Errorf("quantity is required")
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
		if r.LimitPrice == nil || strings.TrimSpace(r.GetLimitPrice()) == "" {
			return fmt.Errorf("limit_price is required for limit orders")
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

func (r *SubmitTargetReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.EventId) == "" ||
		strings.TrimSpace(r.ExecutionId) == "" ||
		strings.TrimSpace(r.ExecutionBindingId) == "" ||
		strings.TrimSpace(r.ExchangeAccountId) == "" {
		return fmt.Errorf("event_id, execution_id, execution_binding_id and exchange_account_id are required")
	}
	if r.CommandSequence == 0 || len(r.Targets) == 0 {
		return fmt.Errorf("command_sequence and targets are required")
	}
	for i, target := range r.Targets {
		if target == nil ||
			strings.TrimSpace(target.Symbol) == "" ||
			strings.TrimSpace(target.TargetQuantity) == "" {
			return fmt.Errorf("targets[%d].symbol and target_quantity are required", i)
		}
	}
	return nil
}
