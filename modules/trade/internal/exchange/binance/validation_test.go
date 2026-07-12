package binance

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdapter_PlaceOrder_InvalidRequest_ShouldReject(t *testing.T) {
	a := &Adapter{}
	_, err := a.PlaceOrder(context.Background(), exchange.Credential{}, nil)
	assert.ErrorIs(t, err, errInvalidParam)
	_, err = a.PlaceOrder(context.Background(), exchange.Credential{}, &exchange.PlaceOrderReq{})
	assert.ErrorIs(t, err, errInvalidParam)
}

func TestAdapter_CancelOrder_InvalidRequest_ShouldReject(t *testing.T) {
	a := &Adapter{}
	_, err := a.CancelOrder(context.Background(), exchange.Credential{}, nil)
	assert.ErrorIs(t, err, errInvalidParam)
}

func TestAdapter_NotImplementedAndPrivateStreamValidation_ShouldReturnErrors(t *testing.T) {
	a := &Adapter{}
	err := a.ClosePosition(context.Background(), exchange.Credential{}, exchange.MarketSpot, "BTCUSDT", "")
	assert.ErrorIs(t, err, errNotImplemented)

	_, err = a.ListFundFlows(context.Background(), exchange.Credential{}, nil)
	assert.ErrorIs(t, err, errNotImplemented)

	err = a.SubscribePrivate(context.Background(), exchange.Credential{}, exchange.MarketSpot, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler is required")
}

func TestBinanceOrderToDomain_MapsFields(t *testing.T) {
	got := binanceOrderToDomain(&binanceOrderInfo{
		OrderID: 99, ClientOrderID: "c1", Symbol: "BTCUSDT", Side: "BUY", Type: "LIMIT",
		OrigQty: "1", ExecutedQty: "0.5", Price: "100", Status: "PARTIALLY_FILLED",
	}, exchange.MarketSpot)
	require.NotNil(t, got)
	assert.Equal(t, "c1", got.ClientOrderID)
	assert.Equal(t, exchange.StatusPartiallyFilled, got.Status)
}

func TestLower_ShouldLowercase(t *testing.T) {
	assert.Equal(t, "buy", lower("BUY"))
}
