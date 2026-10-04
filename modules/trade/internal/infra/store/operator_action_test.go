package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorActionIDIsIdempotentAndConflictingRequestFails(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "")
	ctx := context.Background()
	record := OperatorActionRecord{
		SpaceID: "space-1", ActionID: "action-1",
		LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		Reason: "risk", RequestJSON: `{"deadline":10}`, Status: "RUNNING",
	}
	current, created, err := s.CreateOperatorAction(ctx, record)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, `{"deadline":10}`, current.RequestJSON)

	current, created, err = s.CreateOperatorAction(ctx, record)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "RUNNING", current.Status)

	record.RequestJSON = `{"deadline":20}`
	_, created, err = s.CreateOperatorAction(ctx, record)
	require.ErrorIs(t, err, ErrConflict)
	require.False(t, created)
}

func TestOperatorActionProgressRoundTrip(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "")
	ctx := context.Background()
	record := OperatorActionRecord{
		SpaceID: "space-1", ActionID: "action-1",
		LogicalAccountID: "logical-1", ActionType: "MANUAL_ORDER",
		Reason: "manual", RequestJSON: `{}`, Status: "RUNNING",
	}
	current, _, err := s.CreateOperatorAction(ctx, record)
	require.NoError(t, err)
	result := `{"order_id":"order-1"}`
	current.Status = "COMPLETED"
	current.ResultJSON = &result
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateOperatorAction(current)
	}))

	got, err := s.GetOperatorAction(ctx, "space-1", "action-1")
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", got.Status)
	require.NotNil(t, got.ResultJSON)
	require.JSONEq(t, result, *got.ResultJSON)
}

func TestOperatorActionConflictRollsBackAtomicPause(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "")
	ctx := context.Background()
	record := OperatorActionRecord{
		SpaceID: "space-1", ActionID: "action-1",
		LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		Reason: "first", RequestJSON: `{"deadline":10}`, Status: "RUNNING",
	}
	_, _, err := s.CreateOperatorAction(ctx, record)
	require.NoError(t, err)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.SetLogicalAccountAutomation(
			"space-1",
			"logical-1",
			"ACTIVE",
			"",
		)
	}))

	conflict := record
	conflict.Reason = "other"
	conflict.RequestJSON = `{"deadline":20}`
	err = s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			"space-1",
			"logical-1",
			"PAUSED",
			"operator",
		); err != nil {
			return err
		}
		_, _, err := tx.EnsureOperatorAction(conflict)
		return err
	})
	require.ErrorIs(t, err, ErrConflict)

	account, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	require.Empty(t, account.PauseReason)
}
