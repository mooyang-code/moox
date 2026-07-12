package rebalance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	domain "github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_Create_IncompleteSnapshots_ShouldReturnError(t *testing.T) {
	svc := Service{}
	err := svc.Create(context.Background(), CreateInput{
		Mode:     domain.FullTarget,
		Targets:  []domain.Target{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Currents: []domain.Current{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "incomplete rebalance snapshots")
}

func TestService_Create_MissingMarketSnapshot_ShouldReturnError(t *testing.T) {
	svc := Service{}
	err := svc.Create(context.Background(), CreateInput{
		SpaceID:            "space-1",
		RunID:              "run-1",
		IdempotencyKey:     "idem-1",
		MarketSnapshotID:   "mkt-1",
		PositionSnapshotID: "pos-1",
		Mode:               domain.FullTarget,
		Targets:            []domain.Target{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Currents:           []domain.Current{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
		Markets:            map[string]Market{},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "market snapshot missing")
}

func TestService_Create_ValidInput_ShouldPersistRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	svc := Service{Store: s}
	err = svc.Create(context.Background(), CreateInput{
		SpaceID:            "space-1",
		RunID:              "run-1",
		IdempotencyKey:     "idem-1",
		MarketSnapshotID:   "mkt-1",
		PositionSnapshotID: "pos-1",
		Mode:               domain.FullTarget,
		Targets:            []domain.Target{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Currents:           []domain.Current{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
		Markets: map[string]Market{
			"BTC-USDT": {MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "100"},
		},
	})
	require.NoError(t, err)

	legs, err := s.ListRebalanceLegs(context.Background(), "space-1", "run-1")
	require.NoError(t, err)
	require.Len(t, legs, 1)
	assert.Equal(t, "PLANNED", legs[0].Status)
}

func seedLedgerUSDT(t *testing.T, s *store.Store, space, account, amount string) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger(space, ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:deposit"),
			BizType: "seed",
			RefType: "test",
			RefID:   "deposit",
			Entries: []ledger.Entry{
				{AccountID: "exchange-clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal(amount).Neg()},
				{AccountID: account, Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal(amount)},
			},
		})
	}))
}

func TestService_Advance_PlannedLeg_ShouldSubmitOrder(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	svc := Service{
		Store:  s,
		Engine: &command.Engine{Store: s},
	}
	require.NoError(t, svc.Create(ctx, CreateInput{
		SpaceID:            "space-1",
		RunID:              "run-1",
		IdempotencyKey:     "idem-1",
		AccountID:          "acct-1",
		ChannelID:          "chan-1",
		MarketSnapshotID:   "mkt-1",
		PositionSnapshotID: "pos-1",
		Mode:               domain.FullTarget,
		Targets:            []domain.Target{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Currents:           []domain.Current{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
		Markets: map[string]Market{
			"BTC-USDT": {MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "100"},
		},
	}))
	seedLedgerUSDT(t, s, "space-1", "acct-1", "1000")

	status, err := svc.Advance(ctx, "space-1", "run-1", "acct-1", "chan-1")
	require.NoError(t, err)
	assert.Equal(t, "EXECUTING", status)

	legs, err := s.ListRebalanceLegs(ctx, "space-1", "run-1")
	require.NoError(t, err)
	assert.Equal(t, "SUBMITTED", legs[0].Status)
	assert.NotEmpty(t, legs[0].PlanID)
}

func TestService_Advance_RejectedOrder_ShouldMarkFailed(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	svc := Service{Store: s}
	require.NoError(t, svc.Create(ctx, CreateInput{
		SpaceID:            "space-1",
		RunID:              "run-2",
		IdempotencyKey:     "idem-2",
		MarketSnapshotID:   "mkt-1",
		PositionSnapshotID: "pos-1",
		Mode:               domain.FullTarget,
		Targets:            []domain.Target{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Currents:           []domain.Current{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
		Markets: map[string]Market{
			"BTC-USDT": {MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "100"},
		},
	}))
	legs, err := s.ListRebalanceLegs(ctx, "space-1", "run-2")
	require.NoError(t, err)
	planID := legs[0].LegID + "-order"
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(&store.OrderRecord{
			SpaceID:       "space-1",
			OrderID:       planID,
			ClientOrderID: planID,
			AccountID:     "acct-1",
			ChannelID:     "chan-1",
			Symbol:        "BTC-USDT",
			Quantity:      "1",
			State:         string(order.Rejected),
			Version:       1,
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateRebalanceLeg("space-1", legs[0].LegID, "SUBMITTED", planID)
	}))

	status, err := svc.Advance(ctx, "space-1", "run-2", "acct-1", "chan-1")
	require.NoError(t, err)
	assert.Equal(t, "FAILED", status)
}

func TestService_Advance_AllLegsCompleted_ShouldReturnCompleted(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	svc := Service{Store: s}
	require.NoError(t, svc.Create(ctx, CreateInput{
		SpaceID:            "space-1",
		RunID:              "run-3",
		IdempotencyKey:     "idem-3",
		MarketSnapshotID:   "mkt-1",
		PositionSnapshotID: "pos-1",
		Mode:               domain.FullTarget,
		Targets:            []domain.Target{{Symbol: "BTC-USDT", Quantity: shared.Zero()}},
		Currents:           []domain.Current{{Symbol: "BTC-USDT", Quantity: shared.MustDecimal("1")}},
		Markets: map[string]Market{
			"BTC-USDT": {MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "100"},
		},
	}))
	legs, err := s.ListRebalanceLegs(ctx, "space-1", "run-3")
	require.NoError(t, err)
	require.Len(t, legs, 1)
	planID := legs[0].LegID + "-order"
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(&store.OrderRecord{
			SpaceID:        "space-1",
			OrderID:        planID,
			ClientOrderID:  planID,
			AccountID:      "acct-1",
			ChannelID:      "chan-1",
			Symbol:         "BTC-USDT",
			Quantity:       "1",
			FilledQuantity: "1",
			State:          string(order.Filled),
			Version:        1,
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateRebalanceLeg("space-1", legs[0].LegID, "SUBMITTED", planID)
	}))

	status, err := svc.Advance(ctx, "space-1", "run-3", "acct-1", "chan-1")
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", status)
}
