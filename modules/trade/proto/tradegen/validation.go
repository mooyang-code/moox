package tradepb

import (
	"fmt"
	"strings"
)

func (r *CreateAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if r.Exchange == Exchange_EXCHANGE_UNSPECIFIED ||
		r.MarketType == MarketType_MARKET_TYPE_UNSPECIFIED ||
		r.ExecutionMode == ExecutionMode_EXECUTION_MODE_UNSPECIFIED {
		return fmt.Errorf("exchange, market_type and execution_mode are required")
	}
	if r.ExecutionMode == ExecutionMode_EXECUTION_MODE_LIVE &&
		strings.TrimSpace(r.CredentialSecretId) == "" {
		return fmt.Errorf("credential_secret_id is required for live execution")
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
	if r.OrderType == OrderType_ORDER_TYPE_UNSPECIFIED ||
		r.Side == OrderSide_ORDER_SIDE_UNSPECIFIED {
		return fmt.Errorf("order_type and side are required")
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
