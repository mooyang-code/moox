package command

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseReservation_ZeroRemaining_ShouldReturnNil(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		AccountID:        "acct-1",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "100",
	}
	err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		return ReleaseReservation(tx, rec)
	})
	assert.NoError(t, err)
}

func TestReleaseReservation_WithRemaining_ShouldPostLedger(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	err = s.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.PostLedger("space-1", ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:deposit"),
			BizType: "seed",
			RefType: "test",
			RefID:   "deposit",
			Entries: []ledger.Entry{
				{AccountID: "exchange-clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("100").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100")},
			},
		}); err != nil {
			return err
		}
		return tx.PostLedger("space-1", ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:freeze"),
			BizType: "seed",
			RefType: "test",
			RefID:   "freeze",
			Entries: []ledger.Entry{
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("60").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "frozen", Amount: shared.MustDecimal("60")},
			},
		})
	})
	require.NoError(t, err)

	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		AccountID:        "acct-1",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "40",
	}
	err = s.Transaction(ctx, func(tx *store.Tx) error {
		return ReleaseReservation(tx, rec)
	})
	assert.NoError(t, err)
}

func TestReleaseReservation_ConsumedExceedsReserved_ShouldReturnError(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		AccountID:        "acct-1",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "150",
	}
	err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		return ReleaseReservation(tx, rec)
	})
	assert.Error(t, err)
}

func TestEngine_AdapterFor_NoResolverNoAdapter_ShouldReturnError(t *testing.T) {
	engine := &Engine{Store: &store.Store{}}
	_, err := engine.AdapterFor(context.Background(), store.OrderRecord{SpaceID: "s", ChannelID: "c"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exchange adapter unavailable")
}
