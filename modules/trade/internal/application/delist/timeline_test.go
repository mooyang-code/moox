package delist

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// timelineFixture 用真实 SQLite 与模拟时钟驱动时间线：持有者查询与清仓发起由测试替换。
type timelineFixture struct {
	t       *testing.T
	store   *store.Store
	service *Service
	now     time.Time
	// holders 是当前的持有者；skipped 是没能判断的账户。
	holders []Holder
	skipped []SkippedAccount
	// busy 为真时发起清仓返回“组合正忙”。
	busy     bool
	launched []FlattenRequest
	changed  int
}

var delistAt = time.Date(2026, 12, 10, 8, 0, 0, 0, time.UTC)

func newTimelineFixture(t *testing.T) *timelineFixture {
	t.Helper()
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "paper", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT", "SWAP"}, logical: "logical-paper"},
		accountSpec{id: "live", exchange: "BINANCE", mode: "LIVE", environment: "PRODUCTION", markets: []string{"SPOT", "SWAP"}, logical: "logical-live"},
	)
	f := &timelineFixture{t: t, store: tradeStore, now: delistAt.Add(-72 * time.Hour)}
	f.service = &Service{
		Store: tradeStore, Now: func() time.Time { return f.now },
		OnChanged: func() { f.changed++ },
		holders: func(context.Context, Key, bool) (HoldersResult, error) {
			return HoldersResult{Holders: append([]Holder(nil), f.holders...), SkippedAccounts: f.skipped}, nil
		},
		Flatten: f.flatten,
	}
	return f
}

// flatten 模拟 FlattenLogicalAccount：同号已有动作时 FAILED 重新进入运行，其余返回旧结果；没有就建一个运行中的动作。
func (f *timelineFixture) flatten(ctx context.Context, request FlattenRequest) error {
	if f.busy {
		return ErrFlattenBusy
	}
	f.launched = append(f.launched, request)
	if action, found, err := f.store.FindOperatorAction(ctx, request.SpaceID, request.ActionID); err != nil || found {
		if err == nil && action.Status == "FAILED" {
			f.setAction(request.SpaceID, request.ActionID, "RUNNING", f.now)
		}
		return err
	}
	_, _, err := f.store.CreateOperatorAction(ctx, store.OperatorActionRecord{
		SpaceID: request.SpaceID, ActionID: request.ActionID, LogicalAccountID: request.LogicalAccountID,
		ActionType: "DELIST_FLATTEN", Reason: request.Reason, RequestJSON: `{"logical_account_id":"` + request.LogicalAccountID + `"}`,
	})
	return err
}

// setAction 直接改动作的状态和最后修改时间（c_mtime 由 SQLite 写入，不受模拟时钟控制）。
func (f *timelineFixture) setAction(spaceID, actionID, status string, mtime time.Time) {
	f.t.Helper()
	require.NoError(f.t, f.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = ?, c_mtime = ? WHERE c_space_id = ? AND c_action_id = ?`,
		status, mtime.UTC().Format("2006-01-02 15:04:05"), spaceID, actionID).Error)
}

func (f *timelineFixture) hold(logicals ...string) {
	f.holders = nil
	for _, logical := range logicals {
		paper := logical == "logical-paper"
		f.holders = append(f.holders, Holder{SpaceID: "space-1", LogicalAccountID: logical, TradingAccountID: strings.TrimPrefix(logical, "logical-"), Quantity: "5", Paper: paper})
	}
}

func (f *timelineFixture) intake(id, scope string, at time.Time, occurred time.Time) IntakeResult {
	f.t.Helper()
	result, err := f.service.Intake(context.Background(), Event{
		EventID: id, OccurredAt: occurred, Key: Key{Exchange: "binance", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"},
		Scope: scope, DelistAt: at, Source: "MANUAL",
	})
	require.NoError(f.t, err)
	return result
}

func (f *timelineFixture) run(at time.Time) {
	f.t.Helper()
	f.now = at
	require.NoError(f.t, f.service.RunAll(context.Background()))
}

func (f *timelineFixture) record(id string) store.DelistRecord {
	f.t.Helper()
	record, err := f.store.GetDelist(context.Background(), id)
	require.NoError(f.t, err)
	return record
}

func (f *timelineFixture) flattens(id string) []store.DelistFlattenRecord {
	f.t.Helper()
	rows, err := f.store.ListDelistFlattens(context.Background(), id)
	require.NoError(f.t, err)
	return rows
}

func (f *timelineFixture) blocked() []store.BlockedInstrumentRecord {
	f.t.Helper()
	rows, err := f.store.ListBlockedInstruments(context.Background())
	require.NoError(f.t, err)
	return rows
}

func (f *timelineFixture) attention() []string {
	f.t.Helper()
	items, err := f.service.Attention(context.Background(), f.now)
	require.NoError(f.t, err)
	return items
}

// 1：新事件在一个事务里建记录、写名单；同一事件 ID 重投不产生第二条。
func TestIntakeCreatesRecordAndBlockOnce(t *testing.T) {
	f := newTimelineFixture(t)
	result := f.intake("evt-1", store.DelistScopePaperOnly, delistAt, f.now)
	require.NotEmpty(t, result.DelistID)
	record := f.record(result.DelistID)
	require.Equal(t, store.DelistActive, record.Status)
	require.Equal(t, 0, record.PlannedRound)
	require.Equal(t, "BINANCE", record.Exchange, "交易所统一大写")
	blocked := f.blocked()
	require.Len(t, blocked, 1)
	require.Equal(t, store.DelistScopePaperOnly, blocked[0].Scope)
	require.Equal(t, result.DelistID, blocked[0].DelistID)
	require.Equal(t, 1, f.changed)

	again := f.intake("evt-1", store.DelistScopePaperOnly, delistAt, f.now)
	require.Empty(t, again.DelistID)
	all, err := f.store.ListDelists(context.Background(), false, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

// 2：下架时间相同、occurred_at 更早都不改动；下架时间不同时旧记录 RESCHEDULED、新记录 ACTIVE，名单行改指新记录。
func TestIntakeReschedulesAndIgnoresStaleEvents(t *testing.T) {
	f := newTimelineFixture(t)
	first := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now)
	require.Empty(t, f.intake("evt-same", store.DelistScopeAll, delistAt, f.now.Add(time.Minute)).DelistID, "下架时间没变")
	require.Empty(t, f.intake("evt-old", store.DelistScopeAll, delistAt.Add(time.Hour), f.now.Add(-time.Minute)).DelistID, "乱序的旧事件")

	later := delistAt.Add(24 * time.Hour)
	second := f.intake("evt-2", store.DelistScopeAll, later, f.now.Add(2*time.Minute))
	require.NotEmpty(t, second.DelistID)
	require.Equal(t, first.DelistID, second.Rescheduled)
	old := f.record(first.DelistID)
	require.Equal(t, store.DelistRescheduled, old.Status)
	require.Contains(t, old.Result, second.DelistID)
	require.NotZero(t, old.FinishedAt)
	require.Equal(t, store.DelistActive, f.record(second.DelistID).Status)
	blocked := f.blocked()
	require.Equal(t, second.DelistID, blocked[0].DelistID, "名单行指向接着处理的记录")
	require.Equal(t, store.DelistScopeAll, blocked[0].Scope)
}

// 3：先 PAPER_ONLY 后 ALL，名单行升级为 ALL；先 ALL 后 PAPER_ONLY，名单行不变。
func TestIntakeMergesScopes(t *testing.T) {
	f := newTimelineFixture(t)
	drill := f.intake("evt-drill", store.DelistScopePaperOnly, delistAt, f.now)
	real := f.intake("evt-real", store.DelistScopeAll, delistAt, f.now)
	require.True(t, real.BlockChanged)
	blocked := f.blocked()
	require.Equal(t, store.DelistScopeAll, blocked[0].Scope)
	require.Equal(t, real.DelistID, blocked[0].DelistID)
	require.Equal(t, store.DelistActive, f.record(drill.DelistID).Status, "两种范围的记录各走各的")

	g := newTimelineFixture(t)
	g.intake("evt-real", store.DelistScopeAll, delistAt, g.now)
	again := g.intake("evt-drill", store.DelistScopePaperOnly, delistAt, g.now)
	require.False(t, again.BlockChanged)
	require.Equal(t, store.DelistScopeAll, g.blocked()[0].Scope)
}

// 5：完整时间线。
func TestTimelinePlansLaunchesAndVerifies(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper", "logical-live")
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID

	f.run(delistAt.Add(-25 * time.Hour))
	require.Equal(t, 0, f.record(id).PlannedRound, "T−24h 之前不做任何事")
	require.Empty(t, f.launched)

	f.run(delistAt.Add(-24 * time.Hour))
	rows := f.flattens(id)
	require.Len(t, rows, 2, "每个持有组合一行")
	require.Equal(t, ActionID(id, 1, "space-1", "logical-live"), rows[0].ActionID, "操作号确定")
	require.Len(t, f.launched, 2)
	require.Equal(t, delistAt.UnixMilli(), f.launched[0].DeadlineAt)
	require.Equal(t, "下架清仓：BINANCE SPOT FOOUSDT（下架记录 "+id+"，第 1 轮）", f.launched[0].Reason)
	require.Equal(t, "FOO-USDT-SPOT", f.launched[0].InstrumentID)
	require.Equal(t, "BINANCE", f.launched[0].Exchange)

	f.run(delistAt.Add(-23 * time.Hour))
	require.Len(t, f.launched, 2, "动作在运行，不再发起")

	f.setAction("space-1", rows[0].ActionID, "COMPLETED", f.now)
	f.setAction("space-1", rows[1].ActionID, "COMPLETED", f.now)
	f.hold("logical-live")
	f.run(delistAt.Add(-2 * time.Hour))
	require.Equal(t, 2, f.record(id).PlannedRound)
	round2 := f.flattens(id)[2:]
	require.Len(t, round2, 1)
	require.NotEqual(t, rows[0].ActionID, round2[0].ActionID, "第 2 轮换新号")
	require.Len(t, f.launched, 3)

	f.setAction("space-1", round2[0].ActionID, "FAILED", delistAt.Add(-time.Hour))
	f.run(delistAt.Add(time.Minute))
	require.Len(t, f.launched, 3, "T 之后不发起")

	f.run(delistAt.Add(10 * time.Minute))
	record := f.record(id)
	require.Equal(t, store.DelistFailed, record.Status, "下架后仍有持有")
	require.Contains(t, f.attention(), "BINANCE SPOT FOOUSDT 下架后核对仍有持有")
	f.now = delistAt.Add(25 * time.Hour)
	require.Empty(t, f.attention(), "结束 24 小时后不再上报")
	require.Len(t, f.blocked(), 1, "真实下架的名单行留着")
}

func TestTimelineVerifyPassesWithoutHolders(t *testing.T) {
	f := newTimelineFixture(t)
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID
	f.run(delistAt.Add(-24 * time.Hour))
	require.Equal(t, 1, f.record(id).PlannedRound, "没有持有者也推进轮次")
	require.Empty(t, f.flattens(id))
	f.run(delistAt.Add(10 * time.Minute))
	require.Equal(t, store.DelistCompleted, f.record(id).Status)

	g := newTimelineFixture(t)
	other := g.intake("evt-1", store.DelistScopeAll, delistAt, g.now).DelistID
	g.skipped = []SkippedAccount{{TradingAccountID: "live", Reason: "读不出资金设置"}}
	g.run(delistAt.Add(10 * time.Minute))
	record := g.record(other)
	require.Equal(t, store.DelistFailed, record.Status, "有没能判断的账户不能当作通过")
	require.Contains(t, record.Result, "live（读不出资金设置）")
}

// 6：错过的时点只规划应到的最新一轮。
func TestTimelinePlansOnlyTheLatestDueRound(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper")
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID
	// [T−24h, T−2h) 期间被门禁挡住（没有推进），T−1h 恢复。
	f.run(delistAt.Add(-time.Hour))
	require.Equal(t, 2, f.record(id).PlannedRound)
	rows := f.flattens(id)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].Round, "错过的第 1 轮不补")

	// 收到时已在 T−2h 之后，同样只规划第 2 轮。
	g := newTimelineFixture(t)
	g.hold("logical-paper")
	g.now = delistAt.Add(-90 * time.Minute)
	late := g.intake("evt-1", store.DelistScopeAll, delistAt, g.now).DelistID
	g.run(g.now)
	require.Equal(t, 2, g.flattens(late)[0].Round)
}

// 7：规划后、发起前“崩溃”，重启后用同一个号发起，只有一个动作；发起后读到 RUNNING 不再发起。
func TestTimelineRestartUsesTheSameActionID(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper")
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID
	f.busy = true
	f.run(delistAt.Add(-24 * time.Hour))
	require.Len(t, f.flattens(id), 1)
	require.Empty(t, f.launched)

	f.busy = false
	f.service = &Service{Store: f.store, Now: f.service.Now, holders: f.service.holders, Flatten: f.flatten}
	f.run(delistAt.Add(-23 * time.Hour))
	f.run(delistAt.Add(-22 * time.Hour))
	require.Len(t, f.launched, 1, "读到 RUNNING 不再发起")
	var count int64
	require.NoError(t, f.store.DBForTest().Raw(`SELECT COUNT(*) FROM t_operator_actions`).Scan(&count).Error)
	require.EqualValues(t, 1, count, "只有一个动作")
	require.Equal(t, f.flattens(id)[0].ActionID, f.launched[0].ActionID)
}

// 8：第 1 轮动作还在跑时第 2 轮拿到忙、不建动作，满 30 分钟出现 delist_failed。
func TestTimelineBusyRowsRaiseAttentionAfterThirtyMinutes(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper")
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID
	f.run(delistAt.Add(-24 * time.Hour))
	f.busy = true
	f.run(delistAt.Add(-2 * time.Hour))
	require.Len(t, f.launched, 1)
	require.Empty(t, f.attention())
	f.run(delistAt.Add(-90 * time.Minute))
	require.Equal(t, []string{"BINANCE SPOT FOOUSDT 第 2 轮组合 logical-paper 30 分钟还没能发起清仓"}, f.attention())
	f.busy = false
	f.run(delistAt.Add(-89 * time.Minute))
	require.Len(t, f.launched, 2)
	require.Equal(t, f.flattens(id)[1].ActionID, f.launched[1].ActionID)
}

// 9：FAILED 的动作不足 10 分钟不重发，满 10 分钟用同一个号重发并重新进入运行。
func TestTimelineRetriesFailedActionsWithTheSameID(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper")
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID
	f.run(delistAt.Add(-24 * time.Hour))
	actionID := f.flattens(id)[0].ActionID
	failedAt := delistAt.Add(-23 * time.Hour)
	f.setAction("space-1", actionID, "FAILED", failedAt)
	require.Equal(t, []string{"BINANCE SPOT FOOUSDT 第 1 轮组合 logical-paper 清仓失败"}, func() []string { f.now = failedAt; return f.attention() }())

	f.run(failedAt.Add(9 * time.Minute))
	require.Len(t, f.launched, 1)
	f.run(failedAt.Add(10 * time.Minute))
	require.Len(t, f.launched, 2)
	require.Equal(t, actionID, f.launched[1].ActionID)
	action, _, err := f.store.FindOperatorAction(context.Background(), "space-1", actionID)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", action.Status)
}

// 10：解除名单把两种范围的进行中记录都取消，之后不再规划、不再核对。
func TestUnblockCancelsRecordsAndStopsTheTimeline(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper")
	real := f.intake("evt-real", store.DelistScopeAll, delistAt, f.now).DelistID
	drill := f.intake("evt-drill", store.DelistScopePaperOnly, delistAt, f.now).DelistID
	removed, err := f.service.Unblock(context.Background(), Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"})
	require.NoError(t, err)
	require.True(t, removed)
	for _, id := range []string{real, drill} {
		record := f.record(id)
		require.Equal(t, store.DelistCanceled, record.Status)
		require.Equal(t, store.DelistUnblockedResult, record.Result)
	}
	f.run(delistAt.Add(-24 * time.Hour))
	f.run(delistAt.Add(time.Hour))
	require.Empty(t, f.launched)
	require.Equal(t, store.DelistCanceled, f.record(real).Status)
}

// 11：演练核对完成后删掉仍是 PAPER_ONLY 的名单行；名单行已被升级为 ALL 时不删。演练只查模拟盘持有者。
func TestDrillEndsByRemovingItsPaperOnlyBlock(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-paper", "logical-live")
	id := f.intake("evt-drill", store.DelistScopePaperOnly, delistAt, f.now).DelistID
	f.run(delistAt.Add(-24 * time.Hour))
	rows := f.flattens(id)
	require.Len(t, rows, 1, "演练只平模拟盘组合")
	require.Equal(t, "logical-paper", rows[0].LogicalAccountID)
	f.hold("logical-live")
	changed := f.changed
	f.run(delistAt.Add(10 * time.Minute))
	require.Equal(t, store.DelistCompleted, f.record(id).Status, "实盘持有不算演练的持有")
	require.Empty(t, f.blocked(), "模拟盘恢复买入")
	require.Equal(t, changed+1, f.changed)

	g := newTimelineFixture(t)
	drill := g.intake("evt-drill", store.DelistScopePaperOnly, delistAt, g.now).DelistID
	g.intake("evt-real", store.DelistScopeAll, delistAt.Add(time.Hour), g.now)
	g.run(delistAt.Add(10 * time.Minute))
	require.Equal(t, store.DelistCompleted, g.record(drill).Status)
	require.Len(t, g.blocked(), 1, "已被真实下架升级为 ALL 的不删")
	require.Equal(t, store.DelistScopeAll, g.blocked()[0].Scope)
}

// 12：收到事件不受暂停影响（暂停只挡定时推进，由定时任务外壳的门禁实现）；13：乱序的旧事件在记录结束后才到，不新建记录、不写名单。
func TestStaleEventAfterTheRecordEndedIsIgnored(t *testing.T) {
	f := newTimelineFixture(t)
	occurred := f.now
	id := f.intake("evt-1", store.DelistScopePaperOnly, delistAt, occurred).DelistID
	f.run(delistAt.Add(10 * time.Minute))
	require.Equal(t, store.DelistCompleted, f.record(id).Status)
	require.Empty(t, f.blocked())

	late := f.intake("evt-0", store.DelistScopePaperOnly, delistAt.Add(-time.Hour), occurred.Add(-time.Hour))
	require.Empty(t, late.DelistID)
	require.Empty(t, f.blocked())
}

// 收到新记录且有组合持有时记 delist_detected，详情是固定文字。
func TestIntakeNotesDetectedWhenHeld(t *testing.T) {
	f := newTimelineFixture(t)
	f.hold("logical-live")
	f.intake("evt-1", store.DelistScopeAll, delistAt, f.now)
	require.Contains(t, telemetry.Recent(telemetry.AttentionDelistDetected, f.now, time.Minute),
		"BINANCE SPOT FOOUSDT 将于 2026-12-10 08:00 UTC 下架，有组合持有")
	telemetry.Forget(telemetry.AttentionDelistDetected, "BINANCE SPOT FOOUSDT 将于 2026-12-10 08:00 UTC 下架，有组合持有")
}

func TestHoldersFailureLeavesTheRecordForTheNextRun(t *testing.T) {
	f := newTimelineFixture(t)
	id := f.intake("evt-1", store.DelistScopeAll, delistAt, f.now).DelistID
	f.service.holders = func(context.Context, Key, bool) (HoldersResult, error) {
		return HoldersResult{}, errors.New("数据库忙")
	}
	f.now = delistAt.Add(10 * time.Minute)
	require.Error(t, f.service.RunAll(context.Background()))
	record := f.record(id)
	require.Equal(t, store.DelistActive, record.Status)
	require.Contains(t, record.Note, "数据库忙")
}
