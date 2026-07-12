package command

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type uniqueExchangeIDAdapter struct {
	cancelStubAdapter
	placeCount int
}

func (a *uniqueExchangeIDAdapter) Place(_ context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	a.placeCount++
	return exchange.ExchangeOrderResult{
		ExchangeOrderID: "ex-new-" + r.ClientOrderID,
		ClientOrderID:   r.ClientOrderID,
		Status:          "OPEN",
	}, nil
}

func TestEngine_AdvanceReplace_OpenReplacement_ShouldCompleteSaga(t *testing.T) {
	adapter := &uniqueExchangeIDAdapter{cancelStubAdapter: cancelStubAdapter{
		cancelResult: exchange.ExchangeOrderResult{Status: "CANCELED"},
	}}
	engine, old := openOpenOrder(t, adapter)
	sagaID := "saga-open"
	rec, err := engine.Replace(context.Background(), sagaID, old.OrderID, PlaceInput{
		SpaceID: "space-1", OrderID: "order-new", ClientOrderID: "client-new",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "102",
	})
	require.NoError(t, err)
	_, err = engine.Submit(context.Background(), "space-1", rec.ReplacementOrderID, "")
	require.NoError(t, err)
	got, err := engine.AdvanceReplace(context.Background(), "space-1", sagaID)
	require.NoError(t, err)
	assert.Equal(t, string(execution.SagaReplacementSubmitted), got.State)
}
