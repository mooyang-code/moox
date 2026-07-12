package okx

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
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

func TestAdapter_CancelAllOrders_NotImplemented_ShouldReturnError(t *testing.T) {
	a := &Adapter{}
	_, err := a.CancelAllOrders(context.Background(), exchange.Credential{}, exchange.MarketSpot, "")
	assert.ErrorIs(t, err, errNotImplemented)
}

func TestAdapter_DustAndPrivateStreamValidation_ShouldReturnErrors(t *testing.T) {
	a := &Adapter{}
	_, err := a.ListConvertibleDustAssets(context.Background(), exchange.Credential{}, nil)
	assert.ErrorIs(t, err, errNotImplemented)

	_, err = a.ConvertDust(context.Background(), exchange.Credential{}, nil)
	assert.ErrorIs(t, err, errNotImplemented)

	err = a.SubscribePrivate(context.Background(), exchange.Credential{}, exchange.MarketSpot, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler is required")
}
