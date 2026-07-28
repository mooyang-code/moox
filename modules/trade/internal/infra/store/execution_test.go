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
		return tx.CreateExchangeAccount(testAccount())
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
		return tx.CreateExchangeAccount(testAccount())
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

func TestPostLedgerUpdatesExistingProjectionInSecondTransaction(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(testAccount())
	}))
	reservation := LedgerTransactionRecord{
		SpaceID: "space-1", TransactionID: "reserve-1",
		ExchangeAccountID: "account-1", TransactionType: LedgerReservation,
		SourceType: "ORDER", SourceID: "order-1",
		Entries: []LedgerEntryRecord{
			{Asset: "USDT", Bucket: "AVAILABLE", Amount: shared.MustDecimal("-10")},
			{Asset: "USDT", Bucket: "RESERVED", Amount: shared.MustDecimal("10")},
		},
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger(reservation)
	}))
	release := reservation
	release.TransactionID = "release-1"
	release.TransactionType = LedgerReservationRelease
	release.SourceID = "order-1-release"
	release.Entries = []LedgerEntryRecord{
		{Asset: "USDT", Bucket: "AVAILABLE", Amount: shared.MustDecimal("10")},
		{Asset: "USDT", Bucket: "RESERVED", Amount: shared.MustDecimal("-10")},
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger(release)
	}))

	projections, err := s.ListBalanceProjections(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Len(t, projections, 2)
	for _, projection := range projections {
		require.Equal(t, "0", projection.Amount.String())
		require.Equal(t, uint64(2), projection.Version)
	}
}

func TestPostLedgerRejectsNonTerminatingDecimalBeforeAnyWrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(testAccount())
	}))
	seed := LedgerTransactionRecord{
		SpaceID: "space-1", TransactionID: "seed-1",
		ExchangeAccountID: "account-1", TransactionType: LedgerSyncAdjustment,
		SourceType: "SYNC", SourceID: "sync-1",
		Entries: []LedgerEntryRecord{
			{Asset: "USDT", Bucket: "AVAILABLE", Amount: shared.MustDecimal("10")},
			{Asset: "USDT", Bucket: "SYNC_OFFSET", Amount: shared.MustDecimal("-10")},
		},
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger(seed)
	}))

	oneThird := shared.MustDecimal("1").Div(shared.MustDecimal("3"))
	fraction := LedgerTransactionRecord{
		SpaceID: "space-1", TransactionID: "fraction-1",
		ExchangeAccountID: "account-1", TransactionType: LedgerSyncAdjustment,
		SourceType: "SYNC", SourceID: "sync-fraction",
		Entries: []LedgerEntryRecord{
			{Asset: "USDT", Bucket: "AVAILABLE", Amount: oneThird},
			{Asset: "USDT", Bucket: "SYNC_OFFSET", Amount: oneThird.Neg()},
		},
	}
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger(fraction)
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	var transactions int64
	require.NoError(t, s.db.Table("t_ledger_transactions").Count(&transactions).Error)
	require.Equal(t, int64(1), transactions)
	projections, err := s.ListBalanceProjections(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Len(t, projections, 2)
	require.Equal(t, "10", projections[0].Amount.String())
	require.Equal(t, "-10", projections[1].Amount.String())
	require.Equal(t, uint64(1), projections[0].Version)
	require.Equal(t, uint64(1), projections[1].Version)
}
