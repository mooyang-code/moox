package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCountOperatorActionsSince(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one",
			ExecutionMode: "PAPER", MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	_, _, err := s.CreateOperatorAction(ctx, OperatorActionRecord{
		SpaceID: "space-1", ActionID: "flatten-1", LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		RequestJSON: `{"logical_account_id":"logical-1"}`, Reason: "stop", Status: "RUNNING",
	})
	require.NoError(t, err)
	count, err := s.CountOperatorActionsSince(ctx, "space-1", "logical-1", "FLATTEN", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	count, err = s.CountOperatorActionsSince(ctx, "space-1", "logical-1", "FLATTEN", time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, count)
}
