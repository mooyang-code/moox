package store

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// acceptTargetForTest 仅用于 store 包内测试直接验证 tx.AcceptLogicalAccountTarget 行为。
func acceptTargetForTest(ctx context.Context, s *Store, record LogicalAccountTargetRecord) (LogicalAccountTargetRecord, bool, error) {
	unlock, err := s.LockLogicalAccountContext(ctx, record.SpaceID, record.LogicalAccountID)
	if err != nil {
		return LogicalAccountTargetRecord{}, false, err
	}
	defer unlock()
	var current LogicalAccountTargetRecord
	var accepted bool
	err = s.Transaction(ctx, func(tx *Tx) error {
		var txErr error
		current, accepted, txErr = tx.AcceptLogicalAccountTarget(record)
		return txErr
	})
	return current, accepted, err
}

func TestAcceptLogicalAccountTargetKeepsLatestBar(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()

	first := validLogicalAccountTarget()
	current, accepted, err := acceptTargetForTest(ctx, s, first)
	require.NoError(t, err)
	require.True(t, accepted)
	require.Equal(t, "1", current.Targets[0].Quantity)

	stale := first
	stale.TargetID = "target-stale"
	setTargetBar(&stale, int64(1))
	stale.Targets = []InstrumentQuantity{{InstrumentID: "BTC-USDT-SPOT", Quantity: "9"}}
	_, accepted, err = acceptTargetForTest(ctx, s, stale)
	require.ErrorIs(t, err, ErrConflict)
	require.False(t, accepted)

	next := first
	next.TargetID = "target-2"
	setTargetBar(&next, int64(2))
	next.Targets = []InstrumentQuantity{{InstrumentID: "BTC-USDT-SPOT", Quantity: "2.0"}}
	current, accepted, err = acceptTargetForTest(ctx, s, next)
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
	record.Targets = []InstrumentQuantity{}

	current, accepted, err := acceptTargetForTest(
		context.Background(),
		s,
		record,
	)

	require.NoError(t, err)
	require.True(t, accepted)
	require.Empty(t, current.Targets)
	account, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
}

// 接收按会话开始时刻比较（批 3 的 3.6）：组合上没有目标行时直接接受（不看归属）；更早的会话按被取代拒绝；
// 开始时刻相同而会话不同按会话冲突拒绝；更晚的会话覆盖；开始时刻不大于 c_closed_through 的会话一律按被取代拒绝。
func TestAcceptLogicalAccountTargetOrdersSessionsByStartTime(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-owner")
	ctx := context.Background()
	record := func(targetID, instanceID, sessionID string, startedAt int64) LogicalAccountTargetRecord {
		value := validLogicalAccountTarget()
		value.TargetID, value.InstanceID, value.SessionID, value.SessionStartedAt = targetID, instanceID, sessionID, startedAt
		return value
	}

	_, accepted, err := acceptTargetForTest(ctx, s, record("target-1", "runner-1", "session-1", 1_000))
	require.NoError(t, err, "没有目标行时不看归属，直接接受")
	require.True(t, accepted)

	_, accepted, err = acceptTargetForTest(ctx, s, record("target-early", "runner-0", "session-0", 999))
	require.ErrorIs(t, err, ErrTargetSuperseded)
	require.False(t, accepted)

	_, accepted, err = acceptTargetForTest(ctx, s, record("target-same-ms", "runner-2", "session-x", 1_000))
	require.ErrorIs(t, err, ErrTargetSessionConflict)
	require.NotErrorIs(t, err, ErrTargetSuperseded)
	require.False(t, accepted)

	_, accepted, err = acceptTargetForTest(ctx, s, record("target-2", "runner-2", "session-2", 2_000))
	require.NoError(t, err)
	require.True(t, accepted)

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		_, _, err := tx.CloseTargetSession("space-1", "logical-1", 3_000, time.Now().UnixMilli())
		return err
	}))
	_, accepted, err = acceptTargetForTest(ctx, s, record("target-3", "runner-3", "session-3", 3_000))
	require.ErrorIs(t, err, ErrTargetSuperseded, "开始时刻不大于 c_closed_through 的会话已关闭")
	require.False(t, accepted)
	_, accepted, err = acceptTargetForTest(ctx, s, record("target-4", "runner-3", "session-4", 3_001))
	require.NoError(t, err)
	require.True(t, accepted)
	current, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "target-4", current.TargetID)
}

func TestAcceptLogicalAccountTargetRejectsNegativeSpotQuantity(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	record := validLogicalAccountTarget()
	record.Targets[0].Quantity = "-1"

	_, accepted, err := acceptTargetForTest(context.Background(), s, record)

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
	record.Targets = []InstrumentQuantity{
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "2.00"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1.0"},
	}
	_, accepted, err := acceptTargetForTest(ctx, s, record)
	require.NoError(t, err)
	require.True(t, accepted)

	record.Targets[0], record.Targets[1] = record.Targets[1], record.Targets[0]
	current, accepted, err := acceptTargetForTest(ctx, s, record)

	require.NoError(t, err)
	require.False(t, accepted)
	require.Equal(t, []InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "2"},
	}, current.Targets)
}

func TestLogicalAccountTargetCASCannotOverwriteNewerBar(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	first := validLogicalAccountTarget()
	_, accepted, err := acceptTargetForTest(ctx, s, first)
	require.NoError(t, err)
	require.True(t, accepted)

	next := first
	next.TargetID = "target-2"
	setTargetBar(&next, int64(2))
	_, accepted, err = acceptTargetForTest(ctx, s, next)
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
			_, _, err := acceptTargetForTest(ctx, s, record)
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
		{"session started at", func(v *LogicalAccountTargetRecord) { v.SessionStartedAt = 0 }},
		{"validity", func(v *LogicalAccountTargetRecord) { v.ValidUntil = v.BarEndTime }},
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
			record.Targets = append([]InstrumentQuantity(nil), record.Targets...)
			tt.mutate(&record)
			_, accepted, err := acceptTargetForTest(context.Background(), s, record)
			require.Error(t, err)
			require.False(t, accepted)
		})
	}
}

// CloseTargetSession 只缩短属于已关闭会话、还没过期的那一行：有效期取“现在”与 bar_end_time 加 1 毫秒中较大的一个；
// 更晚会话的目标、已过期的目标、已置为过期的目标都不动；c_closed_through 只增不减。
func TestCloseTargetSessionShortensOnlyLiveClosedTarget(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, test := range []struct {
		name string
		// barEnd、validUntil 是目标行原来的 bar_end_time 与有效期；startedAt 是目标所属会话的开始时刻；status 非空时先改成它。
		barEnd, validUntil time.Time
		startedAt          int64
		status             string
		shortened          bool
		want               int64
	}{
		{name: "已过 bar_end_time：取现在", barEnd: now.Add(-time.Minute), validUntil: now.Add(time.Hour), startedAt: 1_000, shortened: true, want: now.UnixMilli()},
		{name: "还没到 bar_end_time：取 bar_end_time 加 1 毫秒", barEnd: now.Add(time.Minute), validUntil: now.Add(time.Hour), startedAt: 1_000, shortened: true, want: now.Add(time.Minute).UnixMilli() + 1},
		{name: "更晚会话的目标不动", barEnd: now.Add(-time.Minute), validUntil: now.Add(time.Hour), startedAt: 2_000, want: now.Add(time.Hour).UnixMilli()},
		{name: "已置为过期的目标不动", barEnd: now.Add(-time.Minute), validUntil: now.Add(time.Hour), startedAt: 1_000, status: "EXPIRED", want: now.Add(time.Hour).UnixMilli()},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := openTestStore(t)
			seedLogicalAccount(t, s, "runner-1")
			ctx := context.Background()
			record := validLogicalAccountTarget()
			record.SessionStartedAt, record.BarEndTime, record.ValidUntil = test.startedAt, test.barEnd.UnixMilli(), test.validUntil.UnixMilli()
			current, accepted, err := acceptTargetForTest(ctx, s, record)
			require.NoError(t, err)
			require.True(t, accepted)
			if test.status != "" {
				current.Status = test.status
				updated, err := s.UpdateLogicalAccountTargetState(ctx, current)
				require.NoError(t, err)
				require.True(t, updated)
			}
			var closed int64
			var shortened bool
			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				closed, shortened, err = tx.CloseTargetSession("space-1", "logical-1", 1_000, now.UnixMilli())
				return err
			}))
			require.Equal(t, int64(1_000), closed)
			require.Equal(t, test.shortened, shortened)
			after, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.NoError(t, err, "关闭不删目标行")
			require.Equal(t, test.want, after.ValidUntil)

			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				closed, _, err = tx.CloseTargetSession("space-1", "logical-1", 500, now.UnixMilli())
				return err
			}))
			require.Equal(t, int64(1_000), closed, "c_closed_through 不回退")
		})
	}
}

// targetBarBase 是测试目标的基准 K 线结束时间；目标按 K 线结束时间排序。
const targetBarBase int64 = 1_700_000_000_000

func validLogicalAccountTarget() LogicalAccountTargetRecord {
	record := LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TargetID: "target-1", InstanceID: "runner-1", SessionID: "session-1", SessionStartedAt: 1_000, StrategyID: "strategy-1",
		ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
		Targets: []InstrumentQuantity{{
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
}

// seedCurrentTarget 给 logical-1 写一行 instanceID 的当前目标（空目标、有效一小时）：目标单只能由当前目标行所属的实例落库。
func seedCurrentTarget(tx *Tx, instanceID string) error {
	now := time.Now().UTC()
	_, _, err := tx.AcceptLogicalAccountTarget(LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-current",
		InstanceID: instanceID, SessionID: "session-1", SessionStartedAt: 1_000, StrategyID: "strategy-1",
		BarEndTime: now.Add(-time.Second).UnixMilli(), ValidUntil: now.Add(time.Hour).UnixMilli(),
		Targets: []InstrumentQuantity{}, Status: "PENDING", AcceptedAt: now.UnixMilli(),
	})
	return err
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
			Name:            "logical",
			ExecutionMode:   "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED",
			PauseReason: "新建的组合账户，等待恢复自动执行",
		})
	}))
}

// 保持标的不带数量：编码、受理、读回都保留 hold，现货的负数量检查与编码不把它当作数量目标。
func TestAcceptLogicalAccountTargetKeepsHoldTargetsWithoutQuantity(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	record := validLogicalAccountTarget()
	record.Targets = append(record.Targets, InstrumentQuantity{InstrumentID: "ETH-USDT-SPOT", Hold: true})

	_, accepted, err := acceptTargetForTest(context.Background(), s, record)

	require.NoError(t, err)
	require.True(t, accepted)
	got, err := s.GetLogicalAccountTarget(context.Background(), record.SpaceID, record.LogicalAccountID)
	require.NoError(t, err)
	require.Equal(t, []InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
		{InstrumentID: "ETH-USDT-SPOT", Hold: true},
	}, got.Targets)
}

func TestEncodeInstrumentTargetsRejectsHoldWithQuantity(t *testing.T) {
	_, err := encodeInstrumentTargets([]InstrumentQuantity{{InstrumentID: "ETH-USDT-SPOT", Quantity: "1", Hold: true}})
	require.ErrorIs(t, err, ErrInvalidRecord)
	encoded, err := encodeInstrumentTargets([]InstrumentQuantity{{InstrumentID: "ETH-USDT-SPOT", Hold: true}})
	require.NoError(t, err)
	require.JSONEq(t, `[{"instrument_id":"ETH-USDT-SPOT","hold":true}]`, encoded)
	decoded, err := decodeCurrentTargets(encoded)
	require.NoError(t, err)
	require.Equal(t, []InstrumentQuantity{{InstrumentID: "ETH-USDT-SPOT", Hold: true}}, decoded)
	// 非保持标的的数量仍必须是合法数字。
	_, err = encodeInstrumentTargets([]InstrumentQuantity{{InstrumentID: "ETH-USDT-SPOT"}})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

// 正式目标只有换算过数量的才要求权益快照：全部是保持标的时没有换算，不要求；混有换算项仍要求。
func TestInsertTargetHistoryRequiresEquityOnlyForConvertedTargets(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	receipt := func(id, quantityTargets string) TargetHistoryRecord {
		return TargetHistoryRecord{SpaceID: "space-1", TargetID: id, LogicalAccountID: "logical-1",
			InstanceID: "runner-1", SessionID: "session-1", StrategyID: "strategy-1", BarEndTime: 1, ValidUntil: 2,
			RequestHash: "hash-" + id, SignalTime: 1, WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: quantityTargets, SessionStartedAt: 1, RecordedAt: 1}
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertTargetHistory(receipt("all-hold", `[{"instrument_id":"ETH-USDT-SPOT","hold":true}]`))
	}))
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertTargetHistory(receipt("mixed", `[{"instrument_id":"ETH-USDT-SPOT","hold":true},{"instrument_id":"BTC-USDT-SPOT","quantity":"1"}]`))
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

// 影子目标与正式目标同表：重投查重、换手检查只看正式目标；同一会话同一根 K 线的唯一约束也只管正式目标。
func TestTargetHistoryQueriesSkipShadowRows(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	record := func(id string, barEnd int64) TargetHistoryRecord {
		return TargetHistoryRecord{SpaceID: "space-1", TargetID: id, LogicalAccountID: "logical-1",
			InstanceID: "runner-1", SessionID: "session-1", SessionStartedAt: 1, StrategyID: "strategy-1",
			BarEndTime: barEnd, ValidUntil: barEnd + 60_000, RequestHash: "hash-" + id,
			WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", RecordedAt: barEnd}
	}
	// 两个影子目标落在同一根 K 线上也能记。
	for _, id := range []string{"shadow-1", "shadow-2"} {
		added, err := s.RecordShadowTarget(ctx, record(id, 1_000))
		require.NoError(t, err)
		require.True(t, added)
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.InsertTargetHistory(record("formal-1", 1_000)) }))

	_, err := s.GetAcceptedTargetHistory(ctx, "space-1", "shadow-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	formal, err := s.GetAcceptedTargetHistory(ctx, "space-1", "formal-1")
	require.NoError(t, err)
	require.False(t, formal.Shadow)

	history, err := s.ListAcceptedTargetHistorySince(ctx, "space-1", "logical-1", 0)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "formal-1", history[0].TargetID)

	shadows, err := s.ListShadowTargets(ctx, "space-1", "runner-1", 0)
	require.NoError(t, err)
	require.Len(t, shadows, 2)
	for _, shadow := range shadows {
		require.True(t, shadow.Shadow)
	}
}
