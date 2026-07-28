package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAcceptTargetComparesAndSetsBindingSequence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertExchangeAccount(testAccount())
	}))

	first := TargetExecutionRecord{
		SpaceID: "space-1", ExecutionID: "execution-2", EventID: "event-2",
		ExecutionBindingID: "binding-1", ExchangeAccountID: "account-1",
		CommandSequence: 2, Targets: []TargetPosition{
			{InstrumentID: "btc", Symbol: "BTCUSDT", TargetQuantity: "1"},
		},
		Status: "PENDING",
	}
	accepted, err := s.AcceptTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)

	stale := first
	stale.ExecutionID = "execution-1"
	stale.EventID = "event-1"
	stale.CommandSequence = 1
	stale.Targets[0].TargetQuantity = "9"
	accepted, err = s.AcceptTarget(ctx, stale)
	require.NoError(t, err)
	require.False(t, accepted)

	next := first
	next.ExecutionID = "execution-3"
	next.EventID = "event-3"
	next.CommandSequence = 3
	next.Targets[0].TargetQuantity = "2"
	accepted, err = s.AcceptTarget(ctx, next)
	require.NoError(t, err)
	require.True(t, accepted)

	got, err := s.GetTargetExecutionByBinding(ctx, "space-1", "binding-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), got.CommandSequence)
	require.Equal(t, "execution-3", got.ExecutionID)
	require.Equal(t, "2", got.Targets[0].TargetQuantity)

	var count int64
	require.NoError(t, s.db.Table("t_target_executions").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestAcceptTargetDeduplicatesEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertExchangeAccount(testAccount())
	}))
	target := TargetExecutionRecord{
		SpaceID: "space-1", ExecutionID: "execution-1", EventID: "event-1",
		ExecutionBindingID: "binding-1", ExchangeAccountID: "account-1",
		CommandSequence: 1,
		Targets:         []TargetPosition{{Symbol: "BTCUSDT", TargetQuantity: "1"}},
		Status:          "PENDING",
	}
	accepted, err := s.AcceptTarget(ctx, target)
	require.NoError(t, err)
	require.True(t, accepted)

	target.ExecutionID = "execution-2"
	target.ExecutionBindingID = "binding-2"
	target.CommandSequence = 2
	accepted, err = s.AcceptTarget(ctx, target)
	require.NoError(t, err)
	require.False(t, accepted)
}
