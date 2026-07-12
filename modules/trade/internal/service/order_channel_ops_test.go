package service

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderService_TestChannel_Reachable_ShouldReturnLatency(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	ok, latency, err := svc.TestChannel(context.Background(), "crypto", "ch_1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(12), latency)
}

func TestOrderService_TestChannel_MissingChannel_ShouldFail(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	ok, _, err := svc.TestChannel(context.Background(), "crypto", "missing")
	assert.Error(t, err)
	assert.False(t, ok)
}

func TestOrderService_CancelAllOrders_ValidChannel_ShouldDelegate(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	n, err := svc.CancelAllOrders(context.Background(), "crypto", "ch_1", "BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestOrderService_SetLeverage_ValidInput_ShouldSucceed(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	err := svc.SetLeverage(context.Background(), "crypto", "ch_1", "BTCUSDT", "10")
	assert.NoError(t, err)
}

func TestOrderService_ListInstruments_ShouldReturnRules(t *testing.T) {
	adapter := &fakeExecAdapter{instruments: []exchange.Instrument{{Symbol: "ETHUSDT"}}}
	svc, _ := newExecOrderService(t, adapter)
	got, err := svc.ListInstruments(context.Background(), "crypto", "ch_1", exchange.MarketSpot)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ETHUSDT", got[0].Symbol)
}
