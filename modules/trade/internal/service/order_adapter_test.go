package service

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	_ "github.com/mooyang-code/moox/modules/trade/internal/exchange/all"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderService_NewAdapter_KnownExchange_ShouldReturnAdapter(t *testing.T) {
	svc := New("trade", WithExchangeFactory(exchange.New))
	adapter, err := svc.Order.NewAdapter("binance")
	require.NoError(t, err)
	assert.Equal(t, "binance", adapter.Name())
}

func TestOrderService_NewAdapter_UnknownExchange_ShouldError(t *testing.T) {
	svc := New("trade", WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return nil, errors.New("unknown")
	}))
	_, err := svc.Order.NewAdapter("missing")
	assert.Error(t, err)
}
