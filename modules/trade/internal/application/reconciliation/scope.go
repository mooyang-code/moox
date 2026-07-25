package reconciliation

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
)

type Scope struct {
	SpaceID, AccountID, ChannelID, Symbol, OrderID string
	StartTimeMS, EndTimeMS                         int64
	Limit                                          int
}

type Result struct {
	OrdersScanned int
	FillsApplied  int
}

func (r Reconciler) Scope(ctx context.Context, scope Scope) (Result, error) {
	var result Result
	if r.Store == nil || r.Engine == nil {
		return result, fmt.Errorf("trade reconciliation dependencies are unavailable")
	}
	orders, err := r.Store.ListOrdersForReconciliation(ctx, scope.SpaceID, scope.AccountID, scope.ChannelID, scope.Symbol, scope.OrderID, scope.StartTimeMS, scope.EndTimeMS, scope.Limit)
	if err != nil {
		return result, fmt.Errorf("list reconciliation orders for space=%s account=%s channel=%s symbol=%s: %w", scope.SpaceID, scope.AccountID, scope.ChannelID, scope.Symbol, err)
	}
	handler := consumer.FillHandler{Store: r.Store}
	for _, current := range orders {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if current.ExecutionMode == "paper" {
			continue
		}
		result.OrdersScanned++
		adapter, err := r.Engine.AdapterFor(ctx, current)
		if err != nil {
			return result, fmt.Errorf("resolve reconciliation adapter for order=%s: %w", current.OrderID, err)
		}
		fills, err := adapter.ListFills(ctx, current.Symbol, current.ExchangeOrderID)
		if err != nil {
			return result, fmt.Errorf("list reconciliation fills for order=%s: %w", current.OrderID, err)
		}
		for _, fill := range fills {
			if fill.ExchangeTradeID == "" {
				continue
			}
			applied, err := handler.HandleSource(ctx, current.SpaceID, current.AccountID, current.OrderID, fill.ExchangeTradeID, fill, "reconciliation")
			if err != nil {
				return result, fmt.Errorf("apply reconciliation fill for order=%s trade=%s: %w", current.OrderID, fill.ExchangeTradeID, err)
			}
			if applied {
				result.FillsApplied++
			}
		}
		state, stateErr := adapter.QueryByClientOrderID(ctx, current.Symbol, current.ClientOrderID)
		if stateErr == nil && (state.Status == "CANCELED" || state.Status == "REJECTED" || state.Status == "EXPIRED") {
			if _, err := r.Engine.ReconcileExchangeTerminal(ctx, current.SpaceID, current.OrderID, state.Status); err != nil {
				return result, fmt.Errorf("reconcile terminal order=%s status=%s: %w", current.OrderID, state.Status, err)
			}
		}
	}
	return result, nil
}
