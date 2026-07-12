package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSuccessCursor_ShouldPopulateFields(t *testing.T) {
	now := time.Unix(100, 0)
	account := &Account{AccountID: "acc-1", ChannelID: "ch-1", AccountType: AccountSpot}
	got := successCursor(SyncOptions{Now: now}, account, SyncTypeOrders, "BTCUSDT", 1, 2)
	assert.Equal(t, "acc-1", got.AccountID)
	assert.Equal(t, SyncTypeOrders, got.SyncType)
	assert.Equal(t, int64(2), got.CursorEndMS)
	assert.Equal(t, now, got.LastSuccessAt)
	assert.True(t, got.IsEnabled)
}

func TestFailedCursor_ShouldCaptureError(t *testing.T) {
	account := &Account{AccountID: "acc-1", ChannelID: "ch-1", AccountType: AccountSwap}
	err := errors.New("sync failed")
	got := failedCursor(SyncOptions{}, account, SyncTypeTrades, "ETHUSDT", 10, 20, err)
	assert.Equal(t, "sync failed", got.LastError)
	assert.Equal(t, int64(20), got.CursorEndMS)
	assert.Equal(t, string(AccountSwap), got.MarketType)
}

func TestCursorEnd_NilAndValue_ShouldReturnEndMS(t *testing.T) {
	assert.Equal(t, int64(0), cursorEnd(nil))
	assert.Equal(t, int64(99), cursorEnd(&SyncCursor{CursorEndMS: 99}))
}
