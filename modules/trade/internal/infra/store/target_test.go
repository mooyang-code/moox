package store

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcceptLogicalAccountTargetUsesOwnerAndKeepsMaximumSequence(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()

	first := validLogicalAccountTarget()
	current, accepted, err := s.AcceptLogicalAccountTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)
	require.Equal(t, "1", current.Targets[0].Quantity)

	stale := first
	stale.TargetID = "target-stale"
	stale.CommandSequence = 1
	stale.Targets = []InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "9"}}
	_, accepted, err = s.AcceptLogicalAccountTarget(ctx, stale)
	require.ErrorIs(t, err, ErrConflict)
	require.False(t, accepted)

	next := first
	next.TargetID = "target-2"
	next.CommandSequence = 2
	next.Targets = []InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "2.0"}}
	current, accepted, err = s.AcceptLogicalAccountTarget(ctx, next)
	require.NoError(t, err)
	require.True(t, accepted)
	require.Equal(t, uint64(2), current.CommandSequence)
	require.Equal(t, "2", current.Targets[0].Quantity)

	var count int64
	require.NoError(t, s.db.Table("t_logical_account_targets").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestAcceptLogicalAccountTargetAllowsEmptyFullWhilePaused(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	record := validLogicalAccountTarget()
	record.Targets = []InstrumentTarget{}

	current, accepted, err := s.AcceptLogicalAccountTarget(
		context.Background(),
		record,
	)

	require.NoError(t, err)
	require.True(t, accepted)
	require.Empty(t, current.Targets)
	account, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
}

func TestAcceptLogicalAccountTargetRejectsMismatchedRunner(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-owner")
	record := validLogicalAccountTarget()
	record.RunnerID = "runner-other"

	_, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), record)

	require.ErrorIs(t, err, ErrConflict)
	require.False(t, accepted)
}

func TestAcceptLogicalAccountTargetRejectsNegativeSpotQuantity(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	record := validLogicalAccountTarget()
	record.Targets[0].Quantity = "-1"

	_, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), record)

	require.ErrorIs(t, err, ErrInvalidRecord)
	require.False(t, accepted)
	_, err = s.GetLogicalAccountTarget(
		context.Background(),
		record.SpaceID,
		record.LogicalAccountID,
	)
	require.Error(t, err)
}

func TestAcceptLogicalAccountTargetIsIdempotentForSamePayload(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	record := validLogicalAccountTarget()
	record.Targets = []InstrumentTarget{
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "2.00"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1.0"},
	}
	_, accepted, err := s.AcceptLogicalAccountTarget(ctx, record)
	require.NoError(t, err)
	require.True(t, accepted)

	record.Targets[0], record.Targets[1] = record.Targets[1], record.Targets[0]
	current, accepted, err := s.AcceptLogicalAccountTarget(ctx, record)

	require.NoError(t, err)
	require.False(t, accepted)
	require.Equal(t, []InstrumentTarget{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "2"},
	}, current.Targets)
}

func TestLogicalAccountTargetCASCannotOverwriteNewSequence(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	first := validLogicalAccountTarget()
	_, accepted, err := s.AcceptLogicalAccountTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)

	next := first
	next.TargetID = "target-2"
	next.CommandSequence = 2
	_, accepted, err = s.AcceptLogicalAccountTarget(ctx, next)
	require.NoError(t, err)
	require.True(t, accepted)

	first.Status = "CONVERGED"
	updated, err := s.UpdateLogicalAccountTargetState(ctx, first)
	require.NoError(t, err)
	require.False(t, updated)

	current, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "target-2", current.TargetID)
	require.Equal(t, "PENDING", current.Status)
}

func TestAcceptLogicalAccountTargetConcurrentSequencesKeepMaximum(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	var wait sync.WaitGroup
	errs := make(chan error, 10)
	for sequence := uint64(1); sequence <= 10; sequence++ {
		wait.Add(1)
		go func(sequence uint64) {
			defer wait.Done()
			record := validLogicalAccountTarget()
			record.TargetID = "target-" + strconv.FormatUint(sequence, 10)
			record.CommandSequence = sequence
			record.Targets[0].Quantity = strconv.FormatUint(sequence, 10)
			_, _, err := s.AcceptLogicalAccountTarget(ctx, record)
			if errors.Is(err, ErrConflict) {
				err = nil
			}
			errs <- err
		}(sequence)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	current, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, uint64(10), current.CommandSequence)
	require.Equal(t, "10", current.Targets[0].Quantity)
}

func TestAcceptLogicalAccountTargetRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LogicalAccountTargetRecord)
	}{
		{"space", func(v *LogicalAccountTargetRecord) { v.SpaceID = " " }},
		{"logical account", func(v *LogicalAccountTargetRecord) { v.LogicalAccountID = "" }},
		{"target", func(v *LogicalAccountTargetRecord) { v.TargetID = "" }},
		{"runner", func(v *LogicalAccountTargetRecord) { v.RunnerID = "\t" }},
		{"sequence", func(v *LogicalAccountTargetRecord) { v.CommandSequence = 0 }},
		{"sequence overflow", func(v *LogicalAccountTargetRecord) {
			v.CommandSequence = uint64(math.MaxInt64) + 1
		}},
		{"accepted time", func(v *LogicalAccountTargetRecord) { v.AcceptedAt = 0 }},
		{"status", func(v *LogicalAccountTargetRecord) { v.Status = "RUNNING" }},
		{"instrument", func(v *LogicalAccountTargetRecord) { v.Targets[0].InstrumentID = "" }},
		{"quantity", func(v *LogicalAccountTargetRecord) { v.Targets[0].Quantity = "many" }},
		{"duplicate", func(v *LogicalAccountTargetRecord) {
			v.Targets = append(v.Targets, v.Targets[0])
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			seedLogicalAccount(t, s, "runner-1")
			record := validLogicalAccountTarget()
			record.Targets = append([]InstrumentTarget(nil), record.Targets...)
			tt.mutate(&record)
			_, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), record)
			require.Error(t, err)
			require.False(t, accepted)
		})
	}
}

func validLogicalAccountTarget() LogicalAccountTargetRecord {
	return LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TargetID: "target-1", RunnerID: "runner-1",
		CommandSequence: 1,
		Targets: []InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "1.0",
		}},
		Status: "PENDING", BlockedTargets: []BlockedTarget{},
		AcceptedAt: time.Now().UnixMilli(),
	}
}

func seedLogicalAccount(t *testing.T, s *Store, runnerID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			Name: "logical", OwnerRunnerID: runnerID,
			ExecutionMode: "PAPER", MarketType: "SPOT",
			SettlementAsset: "USDT", AutomationState: "PAUSED",
			PauseReason: "new logical account",
		})
	}))
}
