package store

import (
	"context"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcceptTargetComparesAndSetsBindingSequence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(testAccount())
	}))

	first := validTargetExecution()
	first.ExecutionID = "execution-2"
	first.EventID = first.ExecutionID
	first.CommandSequence = 2
	accepted, err := s.AcceptTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)

	stale := first
	stale.ExecutionID = "execution-1"
	stale.EventID = stale.ExecutionID
	stale.CommandSequence = 1
	stale.Targets = append([]TargetPosition(nil), first.Targets...)
	stale.Targets[0].TargetQuantity = "9"
	accepted, err = s.AcceptTarget(ctx, stale)
	require.NoError(t, err)
	require.False(t, accepted)

	next := first
	next.ExecutionID = "execution-3"
	next.EventID = next.ExecutionID
	next.CommandSequence = 3
	next.Targets = append([]TargetPosition(nil), first.Targets...)
	next.Targets[0].TargetQuantity = "2.0"
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
		return tx.CreateExchangeAccount(testAccount())
	}))
	target := validTargetExecution()
	accepted, err := s.AcceptTarget(ctx, target)
	require.NoError(t, err)
	require.True(t, accepted)

	target.ExecutionBindingID = "binding-2"
	target.CommandSequence = 2
	accepted, err = s.AcceptTarget(ctx, target)
	require.NoError(t, err)
	require.False(t, accepted)
}

func TestAcceptTargetConcurrentSequencesKeepMaximum(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(testAccount())
	}))
	var wait sync.WaitGroup
	errors := make(chan error, 10)
	for sequence := uint64(1); sequence <= 10; sequence++ {
		wait.Add(1)
		go func(sequence uint64) {
			defer wait.Done()
			record := validTargetExecution()
			record.ExecutionID = "execution-" + strconv.FormatUint(sequence, 10)
			record.EventID = record.ExecutionID
			record.CommandSequence = sequence
			record.Targets[0].TargetQuantity = strconv.FormatUint(sequence, 10)
			_, err := s.AcceptTarget(ctx, record)
			errors <- err
		}(sequence)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	got, err := s.GetTargetExecutionByBinding(ctx, "space-1", "binding-1")
	require.NoError(t, err)
	require.Equal(t, uint64(10), got.CommandSequence)
	require.Equal(t, "10", got.Targets[0].TargetQuantity)
}

func TestAcceptTargetRejectsIncompleteOrContradictoryIntent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(testAccount())
	}))
	tests := []struct {
		name   string
		mutate func(*TargetExecutionRecord)
	}{
		{"event identity", func(v *TargetExecutionRecord) { v.EventID = "other" }},
		{"strategy run", func(v *TargetExecutionRecord) { v.StrategyRunID = "" }},
		{"data revision", func(v *TargetExecutionRecord) { v.DataRevision = "" }},
		{"sequence", func(v *TargetExecutionRecord) { v.CommandSequence = 0 }},
		{"sequence overflow", func(v *TargetExecutionRecord) {
			v.CommandSequence = uint64(math.MaxInt64) + 1
		}},
		{"expiry", func(v *TargetExecutionRecord) {
			v.NotAfter = time.Now().Add(-time.Second).UnixMilli()
		}},
		{"instrument", func(v *TargetExecutionRecord) { v.Targets[0].InstrumentID = "" }},
		{"symbol", func(v *TargetExecutionRecord) { v.Targets[0].Symbol = "" }},
		{"decimal", func(v *TargetExecutionRecord) { v.Targets[0].TargetQuantity = "many" }},
		{"duplicate symbol", func(v *TargetExecutionRecord) {
			v.Targets = append(v.Targets, TargetPosition{
				InstrumentID: "btc-2", Symbol: "BTCUSDT", TargetQuantity: "2",
			})
		}},
		{"whitespace space", func(v *TargetExecutionRecord) { v.SpaceID = " \t" }},
		{"whitespace execution", func(v *TargetExecutionRecord) {
			v.ExecutionID, v.EventID = "  ", "  "
		}},
		{"whitespace event", func(v *TargetExecutionRecord) { v.EventID = "\n" }},
		{"whitespace strategy run", func(v *TargetExecutionRecord) { v.StrategyRunID = " " }},
		{"whitespace binding", func(v *TargetExecutionRecord) { v.ExecutionBindingID = "\t" }},
		{"whitespace account", func(v *TargetExecutionRecord) { v.ExchangeAccountID = "\n" }},
		{"whitespace revision", func(v *TargetExecutionRecord) { v.DataRevision = "  " }},
		{"unknown status", func(v *TargetExecutionRecord) { v.Status = "PENDING" }},
		{"whitespace instrument", func(v *TargetExecutionRecord) { v.Targets[0].InstrumentID = " " }},
		{"whitespace symbol", func(v *TargetExecutionRecord) { v.Targets[0].Symbol = "\n" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validTargetExecution()
			record.Targets = append([]TargetPosition(nil), record.Targets...)
			tt.mutate(&record)
			accepted, err := s.AcceptTarget(ctx, record)
			require.ErrorIs(t, err, ErrInvalidRecord)
			require.False(t, accepted)
		})
	}
}

func TestAcceptTargetKeepsBindingOnOneExchangeAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	second := testAccount()
	second.ExchangeAccountID = "account-2"
	second.Name = "second"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateExchangeAccount(second)
	}))
	first := validTargetExecution()
	accepted, err := s.AcceptTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)

	next := first
	next.ExecutionID = "execution-2"
	next.EventID = next.ExecutionID
	next.CommandSequence = 2
	next.ExchangeAccountID = "account-2"
	accepted, err = s.AcceptTarget(ctx, next)

	require.ErrorIs(t, err, ErrConflict)
	require.False(t, accepted)
	got, err := s.GetTargetExecutionByBinding(ctx, "space-1", "binding-1")
	require.NoError(t, err)
	require.Equal(t, "account-1", got.ExchangeAccountID)
	require.Equal(t, uint64(1), got.CommandSequence)
}

func TestTargetProgressUpdateCannotOverwriteNewerSequence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(testAccount())
	}))
	first := validTargetExecution()
	first.Status = "RUNNING"
	accepted, err := s.AcceptTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)

	next := first
	next.ExecutionID = "execution-2"
	next.EventID = next.ExecutionID
	next.CommandSequence = 2
	accepted, err = s.AcceptTarget(ctx, next)
	require.NoError(t, err)
	require.True(t, accepted)

	first.Status = "COMPLETED"
	first.Progress = `{"symbols":{}}`
	updated, err := s.UpdateTargetExecutionState(ctx, first)
	require.NoError(t, err)
	require.False(t, updated)

	got, err := s.GetTargetExecutionByBinding(ctx, "space-1", "binding-1")
	require.NoError(t, err)
	require.Equal(t, "execution-2", got.ExecutionID)
	require.Equal(t, "RUNNING", got.Status)
	rows, err := s.ListTargetExecutions(ctx, "RUNNING")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "execution-2", rows[0].ExecutionID)
}

func validTargetExecution() TargetExecutionRecord {
	return TargetExecutionRecord{
		SpaceID: "space-1", ExecutionID: "execution-1", EventID: "execution-1",
		StrategyRunID: "strategy-run-1", DataRevision: "revision-1",
		ExecutionBindingID: "binding-1", ExchangeAccountID: "account-1",
		CommandSequence: 1, NotAfter: time.Now().Add(time.Minute).UnixMilli(),
		Targets: []TargetPosition{{
			InstrumentID: "btc", Symbol: "BTCUSDT", TargetQuantity: "1.0",
		}},
		Status: "RUNNING",
	}
}
