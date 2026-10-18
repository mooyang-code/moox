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

func TestAcceptLogicalAccountTargetUsesOwnerAndKeepsLatestBar(t *testing.T) {
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
	setTargetBar(&stale, int64(1))
	stale.Targets = []InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "9"}}
	_, accepted, err = s.AcceptLogicalAccountTarget(ctx, stale)
	require.ErrorIs(t, err, ErrConflict)
	require.False(t, accepted)

	next := first
	next.TargetID = "target-2"
	setTargetBar(&next, int64(2))
	next.Targets = []InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "2.0"}}
	current, accepted, err = s.AcceptLogicalAccountTarget(ctx, next)
	require.NoError(t, err)
	require.True(t, accepted)
	require.Equal(t, targetBarBase+2, current.BarEndTime)
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

func TestAcceptLogicalAccountTargetRejectsMismatchedSession(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-owner")
	record := validLogicalAccountTarget()

	_, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), record)

	require.ErrorIs(t, err, ErrTargetAuthorization)
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

func TestLogicalAccountTargetCASCannotOverwriteNewerBar(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	first := validLogicalAccountTarget()
	_, accepted, err := s.AcceptLogicalAccountTarget(ctx, first)
	require.NoError(t, err)
	require.True(t, accepted)

	next := first
	next.TargetID = "target-2"
	setTargetBar(&next, int64(2))
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

func TestAcceptLogicalAccountTargetConcurrentBarsKeepLatest(t *testing.T) {
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
			setTargetBar(&record, int64(sequence))
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
	require.Equal(t, targetBarBase+10, current.BarEndTime)
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
		{"instance", func(v *LogicalAccountTargetRecord) { v.InstanceID = "" }},
		{"session", func(v *LogicalAccountTargetRecord) { v.SessionID = "" }},
		{"validity", func(v *LogicalAccountTargetRecord) { v.ValidUntil = v.EffectiveAt }},
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

// targetBarBase 是测试目标的基准 K 线结束时间；目标按 K 线结束时间排序。
const targetBarBase int64 = 1_700_000_000_000

func validLogicalAccountTarget() LogicalAccountTargetRecord {
	record := LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TargetID: "target-1", InstanceID: "runner-1", SessionID: "session-1", StrategyID: "strategy-1",
		ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
		Targets: []InstrumentTarget{{
			InstrumentID: "BTC-USDT-SPOT", Quantity: "1.0",
		}},
		Status: "PENDING", BlockedTargets: []BlockedTarget{},
		AcceptedAt: time.Now().UnixMilli(),
	}
	setTargetBar(&record, 1)
	return record
}

// setTargetBar 把目标放在基准之后第 n 根 K 线。
func setTargetBar(record *LogicalAccountTargetRecord, n int64) {
	record.BarEndTime = targetBarBase + n
	record.EffectiveAt = record.BarEndTime
	record.CommandSequence = 0
}

func seedLogicalAccount(t *testing.T, s *Store, runnerID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			Name: "logical", OwnerInstanceID: runnerID, OwnerSessionID: "session-1",
			ExecutionMode: "PAPER", MarketType: "SPOT",
			SettlementAsset: "USDT", AutomationState: "PAUSED",
			PauseReason: "new logical account",
		})
	}))
}

// 保持标的不带数量：编码、受理、读回都保留 hold，现货的负数量检查与编码不把它当作数量目标。
func TestAcceptLogicalAccountTargetKeepsHoldTargetsWithoutQuantity(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	record := validLogicalAccountTarget()
	record.Targets = append(record.Targets, InstrumentTarget{InstrumentID: "ETH-USDT-SPOT", Hold: true})

	_, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), record)

	require.NoError(t, err)
	require.True(t, accepted)
	got, err := s.GetLogicalAccountTarget(context.Background(), record.SpaceID, record.LogicalAccountID)
	require.NoError(t, err)
	require.Equal(t, []InstrumentTarget{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
		{InstrumentID: "ETH-USDT-SPOT", Hold: true},
	}, got.Targets)
}

func TestEncodeInstrumentTargetsRejectsHoldWithQuantity(t *testing.T) {
	_, err := encodeInstrumentTargets([]InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "1", Hold: true}})
	require.ErrorIs(t, err, ErrInvalidRecord)
	encoded, err := encodeInstrumentTargets([]InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Hold: true}})
	require.NoError(t, err)
	require.JSONEq(t, `[{"instrument_id":"ETH-USDT-SPOT","hold":true}]`, encoded)
	decoded, err := decodeCurrentTargets(encoded)
	require.NoError(t, err)
	require.Equal(t, []InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Hold: true}}, decoded)
	// 非保持标的的数量仍必须是合法数字。
	_, err = encodeInstrumentTargets([]InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT"}})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

// 回执只有换算过数量的目标才要求权益快照：全部是保持标的时没有换算，不要求；混有换算项仍要求。
func TestInsertTargetReceiptRequiresEquityOnlyForConvertedTargets(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	receipt := func(id, quantityTargets string) TargetReceiptRecord {
		return TargetReceiptRecord{SpaceID: "space-1", TargetID: id, LogicalAccountID: "logical-1", RunnerID: "runner-1", CommandSequence: 1,
			RequestHash: "hash-" + id, SignalTime: 1, WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: quantityTargets, AcceptedAt: 1}
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertTargetReceipt(receipt("all-hold", `[{"instrument_id":"ETH-USDT-SPOT","hold":true}]`))
	}))
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertTargetReceipt(receipt("mixed", `[{"instrument_id":"ETH-USDT-SPOT","hold":true},{"instrument_id":"BTC-USDT-SPOT","quantity":"1"}]`))
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}
