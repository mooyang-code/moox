package eventconsumer

import (
	"context"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"testing"
	"time"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

// 关闭策略会话之后的接收（批 3 的 3.6 第 3 步）：开始时刻不大于组合 c_closed_through 的会话的目标，
// 不管是同会话更晚一根、延迟到达的旧目标、还是同一份目标的重复投递，都按被取代结束，不告警、不换算、不唤醒执行器；
// 关闭不删目标行，当前目标仍是关闭前的那一行。关闭走 Trade 的服务（CloseTargetSession 背后的那一步），不直接改库。

// closeViaTrade 用 logicalaccount.Service.CloseTargetSession 关闭 runner-1 的 sessionID，返回关闭之后的 c_closed_through。
func closeViaTrade(t *testing.T, db *store.Store, sessionID string) int64 {
	t.Helper()
	closed, err := (&logicalapp.Service{Store: db}).CloseTargetSession(
		context.Background(), "space-1", "logical-1", "runner-1", sessionID, testSessionStartedAt(sessionID))
	require.NoError(t, err)
	return closed
}

// closedTargetOptions 是关闭之后的投递选项：被结束的目标不能进入换算、不能唤醒执行器。
func closedTargetOptions(t *testing.T, db *store.Store, now time.Time) TargetOptions {
	opts := targetOptions(db, now)
	opts.WeightResolver = targetResolverFunc(func(context.Context, *eventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error) {
		t.Error("已关闭会话的目标不能进入换算")
		return targetapp.WeightConversion{}, nil
	})
	opts.WakeTarget = func(string, string) { t.Error("已关闭会话的目标不能唤醒执行器") }
	return opts
}

func requireSupersededAfterClose(t *testing.T, result jetstream.HandlerResult) {
	t.Helper()
	require.Equal(t, jetstream.TERM, result.Decision, "%v", result.Err)
	require.ErrorIs(t, result.Err, store.ErrTargetSuperseded)
	require.Equal(t, "superseded", targetErrorCode(result))
	require.False(t, attentionCode(targetErrorCode(result)), "被取代是正常情况，不告警")
}

func requireCurrentTarget(t *testing.T, db *store.Store, targetID string) store.LogicalAccountTargetRecord {
	t.Helper()
	current, err := db.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, targetID, current.TargetID)
	return current
}

var oneBTC = []*eventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1"}}

// 关闭后同会话更晚一根的目标按 superseded 结束（同会话按 bar 先后本该接受，关闭把它挡在第 3 步）。
func TestCloseTargetSessionEndsLaterBarOfSameSession(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), logicalTargetDelivery(t, now, "t1", "runner-1", "logical-1", oneBTC), targetOptions(db, now)).Decision)
	require.Equal(t, testSessionStartedAt("session-1"), closeViaTrade(t, db, "session-1"))

	later := now.Add(time.Minute)
	result := HandleTarget(context.Background(), logicalTargetDelivery(t, later, "t2", "runner-1", "logical-1", oneBTC), closedTargetOptions(t, db, later))

	requireSupersededAfterClose(t, result)
	requireCurrentTarget(t, db, "t1")
	_, err := db.GetAcceptedTargetHistory(context.Background(), "space-1", "t2")
	require.Error(t, err, "被结束的目标不写回执")
}

// 关闭后延迟到达的旧目标被结束：组合上有这个会话的目标行时、和组合上从没收到过它的目标时（没有目标行可比，靠 c_closed_through 挡住）都一样。
func TestCloseTargetSessionEndsDelayedOldTarget(t *testing.T) {
	for _, test := range []struct {
		name    string
		withRow bool
	}{
		{name: "组合上有这个会话的目标行", withRow: true},
		{name: "组合上没有目标行"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openTargetStore(t)
			seedLogicalTargetAccount(t, db, true, true)
			now := time.Now().UTC().Truncate(time.Millisecond)
			if test.withRow {
				require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), logicalTargetDelivery(t, now, "t1", "runner-1", "logical-1", oneBTC), targetOptions(db, now)).Decision)
			}
			closeViaTrade(t, db, "session-1")

			delayed := now.Add(-time.Minute)
			result := HandleTarget(context.Background(), logicalTargetDelivery(t, delayed, "t0", "runner-1", "logical-1", oneBTC), closedTargetOptions(t, db, now))

			requireSupersededAfterClose(t, result)
			if test.withRow {
				requireCurrentTarget(t, db, "t1")
				return
			}
			_, err := db.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
			require.Error(t, err, "延迟到达的旧目标不能成为当前目标")
		})
	}
}

// 关闭后同一份目标的重复投递被结束：会话先后的判断在回执检查之前，已有的回执不能让它绕过关闭。
func TestCloseTargetSessionEndsRedeliveryOfSameTarget(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC().Truncate(time.Millisecond)
	delivery := logicalTargetDelivery(t, now, "t1", "runner-1", "logical-1", oneBTC)
	require.Equal(t, jetstream.ACK, HandleTarget(context.Background(), delivery, targetOptions(db, now)).Decision)
	_, err := db.GetAcceptedTargetHistory(context.Background(), "space-1", "t1")
	require.NoError(t, err)
	closeViaTrade(t, db, "session-1")

	result := HandleTarget(context.Background(), delivery, closedTargetOptions(t, db, now))

	requireSupersededAfterClose(t, result)
	current := requireCurrentTarget(t, db, "t1")
	// 有效期取“关闭那一刻”与 bar_end_time 加 1 毫秒中较大的一个：整段用例在同一毫秒里跑完时是后者，比此刻还晚 1 毫秒。
	require.LessOrEqual(t, current.ValidUntil, max(time.Now().UnixMilli(), current.BarEndTime+1), "关闭把这份目标的有效期缩短到了关闭那一刻")
}

// 新会话在旧会话关闭之前到达时被接受并覆盖：开始时刻更晚，直接覆盖旧会话的目标行（不比 bar_end_time）；
// 之后旧会话的关闭只推进 c_closed_through，不缩短新会话的目标行；旧会话的目标照常被结束，新会话的目标照常接受。
func TestNewSessionBeforeOldCloseIsAcceptedAndKept(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx := context.Background()
	require.Equal(t, jetstream.ACK, HandleTarget(ctx, logicalTargetDelivery(t, now, "t1", "runner-1", "logical-1", oneBTC), targetOptions(db, now)).Decision)

	woke := 0
	opts := targetOptions(db, now)
	opts.WakeTarget = func(string, string) { woke++ }
	newSession := acceptanceDelivery(t, now, acceptanceTarget{id: "n1", session: "session-2", barMinutes: -1, weight: "1"})
	require.Equal(t, jetstream.ACK, HandleTarget(ctx, newSession, opts).Decision)
	require.Equal(t, 1, woke, "新会话的目标覆盖之后唤醒执行器，旧挂单由收敛的第 1 阶段撤")
	before := requireCurrentTarget(t, db, "n1")
	require.Equal(t, testSessionStartedAt("session-2"), before.SessionStartedAt)

	require.Equal(t, testSessionStartedAt("session-1"), closeViaTrade(t, db, "session-1"))
	after := requireCurrentTarget(t, db, "n1")
	require.Equal(t, before.ValidUntil, after.ValidUntil, "关闭旧会话不缩短更晚会话的目标")
	require.Equal(t, before.Status, after.Status)

	later := now.Add(time.Minute)
	requireSupersededAfterClose(t, HandleTarget(ctx, logicalTargetDelivery(t, later, "t2", "runner-1", "logical-1", oneBTC), closedTargetOptions(t, db, later)))
	requireCurrentTarget(t, db, "n1")
	next := acceptanceDelivery(t, now, acceptanceTarget{id: "n2", session: "session-2", barMinutes: 1, weight: "2"})
	require.Equal(t, jetstream.ACK, HandleTarget(ctx, next, opts).Decision)
	requireCurrentTarget(t, db, "n2")
}

// 影子目标在关闭后被结束：影子目标只做过期、人工控制、会话已关闭三步，关闭之后同一会话的影子目标按被取代结束、不记录。
func TestShadowTargetEndedAfterClose(t *testing.T) {
	db := openTargetStore(t)
	seedLogicalTargetAccount(t, db, true, true)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx := context.Background()
	require.Equal(t, jetstream.ACK, HandleTarget(ctx, shadowSessionDelivery(t, now, "shadow-1", "session-1", false), targetOptions(db, now)).Decision)
	closeViaTrade(t, db, "session-1")

	opts := closedTargetOptions(t, db, now)
	opts.Wake = func() { t.Error("影子目标不唤醒执行器") }
	result := HandleTarget(ctx, shadowSessionDelivery(t, now.Add(time.Minute), "shadow-2", "session-1", false), opts)

	requireSupersededAfterClose(t, result)
	shadows, err := db.ListShadowTargets(ctx, "space-1", "runner-1", 10)
	require.NoError(t, err)
	require.Len(t, shadows, 1, "关闭之后的影子目标不记录")
	require.Equal(t, "shadow-1", shadows[0].TargetID)
	require.Equal(t, testSessionStartedAt("session-1"), shadows[0].SessionStartedAt)
}
