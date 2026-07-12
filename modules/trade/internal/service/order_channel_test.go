package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderService_CreateChannel_ValidInput_ShouldReturnID(t *testing.T) {
	store := &memoryAccountStore{}
	svc := &OrderService{store: store, exNew: nil}
	ctx := context.Background()
	id, err := svc.CreateChannel(ctx, "crypto", &TradeChannel{
		ChannelName: "binance", Exchange: "binance", AccountID: "acc-1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	got, err := store.GetChannel(ctx, "crypto", id)
	require.NoError(t, err)
	assert.Equal(t, "binance", got.ChannelName)
}

func TestOrderService_CreateChannel_InvalidInput_ShouldReject(t *testing.T) {
	svc := &OrderService{store: &memoryAccountStore{}}
	_, err := svc.CreateChannel(context.Background(), "crypto", &TradeChannel{Exchange: "binance"})
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestOrderService_DeleteChannel_EmptyID_ShouldReject(t *testing.T) {
	svc := &OrderService{store: &memoryAccountStore{}}
	err := svc.DeleteChannel(context.Background(), "crypto", "")
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestService_Health_ShouldReturnModuleName(t *testing.T) {
	svc := New("trade-test")
	got := svc.Health()
	assert.Equal(t, "trade-test", got.Module)
	assert.True(t, got.Ready)
}

func TestService_New_EmptyModule_ShouldDefaultTrade(t *testing.T) {
	svc := New("")
	assert.Equal(t, "trade", svc.module)
}
