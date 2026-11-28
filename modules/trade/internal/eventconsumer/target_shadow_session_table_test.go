package eventconsumer

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// 影子目标的会话检查结果表（批 3 T14 前置 R3）：执行器层会话表（target 包的 TestExecutorSessionTable）的影子目标四行。
// 影子目标在接收时就分流（admitTarget），不写目标行、不进执行器，所以这四行走 HandleTarget，单独成表。
// 和执行器层那张表一样，表里只写“谁是当前会话”这个前提，由 shadowCurrentSession 搭出来；T14 只换它的搭法，结论不变。

// shadowCurrentSession 是这张表唯一的“谁是当前会话”前提：让 runner-1 的 sessionID 成为组合的当前会话，
// 之前的会话随之不再是当前会话。
//
// T14 之前的搭法：把组合的归属（owner）换成这个会话。
// T14 起：影子目标只做过期、人工控制、c_closed_through 三步判断，不和正式目标行比较，
// 所以只写一行新会话的目标行不够，要对之前的会话调 CloseTargetSession 推进 c_closed_through（不写目标行）。
func shadowCurrentSession(t *testing.T, db *store.Store, sessionID string) {
	t.Helper()
	for _, earlier := range testSessions {
		if testSessionStartedAt(earlier) < testSessionStartedAt(sessionID) {
			closeTargetSession(t, db, earlier)
		}
	}
}

// shadowSessionDelivery 是 runner-1 的 sessionID 发来的一份影子目标（BTC-USDT-SPOT 权重 1）；
// expired 为真时 bar_end_time、valid_until 都已过去。
func shadowSessionDelivery(t *testing.T, now time.Time, targetID, sessionID string, expired bool) *jetstream.Delivery {
	t.Helper()
	barEnd, validUntil := now, now.Add(time.Hour)
	if expired {
		barEnd, validUntil = now.Add(-2*time.Hour), now.Add(-time.Hour)
	}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: targetID, InstanceId: "runner-1", SessionId: sessionID, SessionStartedAt: testSessionStartedAt(sessionID), StrategyId: "strategy-1", LogicalAccountId: "logical-1",
		Targets:    []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "spot", TargetWeight: "1"}},
		BarEndTime: barEnd.UnixMilli(), ValidUntil: validUntil.UnixMilli(),
		Shadow: true,
	}, events.PublishOptions{EventID: targetID, OccurredAt: barEnd, SpaceID: "space-1", SubjectID: "logical-1"})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{RawData: raw, Subject: encoded.Subject, RawMessageID: targetID, ContentType: events.ContentType}
}

func TestShadowTargetSessionTable(t *testing.T) {
	for _, test := range []struct {
		name string
		// current 是投递前组合的当前会话。
		current string
		// manual 表示投递前把组合改成人工控制。
		manual   bool
		session  string
		expired  bool
		decision jetstream.HandlerDecision
		code     string
		recorded bool
	}{
		{name: "当前会话的影子目标：记录", current: "session-1", session: "session-1", decision: jetstream.ACK, recorded: true},
		// “别的会话”取同一实例的旧会话（已关闭）：T14 之前换成别的实例时按 authorization_conflict，T14 起不看实例、同样按 superseded。
		{name: "别的会话的影子目标：按被取代结束", current: "session-2", session: "session-1", decision: jetstream.TERM, code: "superseded"},
		{name: "人工控制：拒绝", current: "session-1", manual: true, session: "session-1", decision: jetstream.TERM, code: "authorization_conflict"},
		{name: "过期的影子目标：按过期结束", current: "session-1", session: "session-1", expired: true, decision: jetstream.TERM, code: "target_expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openTargetStore(t)
			seedLogicalTargetAccount(t, db, true, true)
			shadowCurrentSession(t, db, test.current)
			if test.manual {
				require.NoError(t, db.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL', c_pause_source = '', c_pause_reason = '', c_margin_reduce_state = ''").Error)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			woke := false
			opts := targetOptions(db, now)
			opts.Wake = func() { woke = true }
			opts.WakeTarget = func(string, string) { woke = true }

			result := HandleTarget(context.Background(), shadowSessionDelivery(t, now, "shadow-1", test.session, test.expired), opts)

			require.Equal(t, test.decision, result.Decision, "投递结论：%v", result.Err)
			code := ""
			if result.Decision == jetstream.TERM {
				code = targetErrorCode(result)
			}
			require.Equal(t, test.code, code, "拒绝原因")
			shadows, err := db.ListShadowTargets(context.Background(), "space-1", "runner-1", 10)
			require.NoError(t, err)
			if test.recorded {
				require.Len(t, shadows, 1, "影子目标应记进影子目标表")
				require.Equal(t, "shadow-1", shadows[0].TargetID)
				require.Equal(t, test.session, shadows[0].SessionID)
			} else {
				require.Empty(t, shadows, "被拒绝的影子目标不记录")
			}
			_, err = db.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
			require.Error(t, err, "影子目标不能成为组合的正式目标")
			require.False(t, woke, "影子目标不唤醒执行器")
		})
	}
}
