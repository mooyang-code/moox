package store

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

func TestPostLedgerRejectsUnbalancedTransaction(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertExchangeAccount(testAccount())
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger(LedgerTransactionRecord{
			SpaceID: "space-1", TransactionID: "ledger-1",
			ExchangeAccountID: "account-1", TransactionType: LedgerReservation,
			SourceType: "ORDER", SourceID: "order-1",
			Entries: []LedgerEntryRecord{
				{Asset: "USDT", Bucket: "AVAILABLE", Amount: shared.MustDecimal("-10")},
				{Asset: "USDT", Bucket: "RESERVED", Amount: shared.MustDecimal("9")},
			},
		})
	})
	require.ErrorIs(t, err, ErrUnbalancedLedger)

	var count int64
	require.NoError(t, s.db.Table("t_ledger_transactions").Count(&count).Error)
	require.Zero(t, count)
}

func TestPostLedgerUpdatesProjectionsAtomically(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertExchangeAccount(testAccount())
	}))
	transaction := LedgerTransactionRecord{
		SpaceID: "space-1", TransactionID: "ledger-1",
		ExchangeAccountID: "account-1", TransactionType: LedgerReservation,
		SourceType: "ORDER", SourceID: "order-1",
		Entries: []LedgerEntryRecord{
			{Asset: "USDT", Bucket: "AVAILABLE", Amount: shared.MustDecimal("-10.25")},
			{Asset: "USDT", Bucket: "RESERVED", Amount: shared.MustDecimal("10.25")},
		},
	}
	stop := errors.New("rollback")
	err := s.Transaction(ctx, func(tx *Tx) error {
		require.NoError(t, tx.PostLedger(transaction))
		return stop
	})
	require.ErrorIs(t, err, stop)

	var count int64
	require.NoError(t, s.db.Table("t_ledger_entries").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, s.db.Table("t_trade_balance_projections").Count(&count).Error)
	require.Zero(t, count)

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger(transaction)
	}))
	projections, err := s.ListBalanceProjections(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Len(t, projections, 2)
	require.Equal(t, "-10.25", projections[0].Amount.String())
	require.Equal(t, "10.25", projections[1].Amount.String())
}
