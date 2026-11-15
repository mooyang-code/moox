package eventconsumer

import (
	"context"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 目标接收的结果表（批 3 T0 第 4 项）：把同会话新旧目标、同一目标重复投递、换会话、人工控制、过期几种情形的
// 接受与拒绝结果固定下来。之后改“谁来挡住旧会话”的做法时只改每个用例的前置步骤，这张表里的结果不变；
// 唯一的例外是“会话关闭之后的重复投递”那一行，见用例上的说明。
// 每一步断言四件事：投递结论（ACK、TERM、RETRY）、拒绝原因码、是否唤醒执行器、之后组合上的当前目标。

// acceptanceTarget 描述一条目标消息。时间用相对“现在”的分钟数：存储层用真实时钟判断有效期，这里只取相对值，结果不依赖具体时刻。
type acceptanceTarget struct {
	id       string
	instance string // 空表示 runner-1
	session  string // 空表示 session-1
	// barMinutes 是 bar_end_time 相对现在的分钟数；validMinutes 是 valid_until 相对现在的分钟数，0 表示 bar_end_time 之后一小时。
	barMinutes   int
	validMinutes int
	// weight 非空时目标里带一项 BTC-USDT-SPOT；空表示空目标（清仓）。
	weight string
}

type acceptanceStep struct {
	note string
	// before 是投递前的前置操作（换会话、改控制模式等）。
	before func(t *testing.T, db *store.Store)
	target acceptanceTarget
	// intakeClosed 表示投递时 Trade 处于暂停（不接收真实目标）。
	intakeClosed bool
	decision     jetstream.HandlerDecision
	// code 是拒绝原因码；接受与重投时为空。
	code string
	woke bool
	// current 是这一步之后组合上的当前目标 ID；空表示没有目标行。
	current string
}

func acceptanceDelivery(t *testing.T, now time.Time, spec acceptanceTarget) *jetstream.Delivery {
	t.Helper()
	instance, session := spec.instance, spec.session
	if instance == "" {
		instance = "runner-1"
	}
	if session == "" {
		session = "session-1"
	}
	barEnd := now.Add(time.Duration(spec.barMinutes) * time.Minute)
	validUntil := barEnd.Add(time.Hour)
	if spec.validMinutes != 0 {
		validUntil = now.Add(time.Duration(spec.validMinutes) * time.Minute)
	}
	var targets []*tradeeventpb.InstrumentWeightTarget
	if spec.weight != "" {
		targets = []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", MarketType: "SPOT", TargetWeight: spec.weight}}
	}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: spec.id, InstanceId: instance, SessionId: session, StrategyId: "strategy-1", LogicalAccountId: "logical-1",
		Targets: targets, BarEndTime: timestamppb.New(barEnd), ValidUntil: timestamppb.New(validUntil),
	}, events.PublishOptions{EventID: spec.id, OccurredAt: barEnd, SpaceID: "space-1", SubjectID: "logical-1"})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{RawData: raw, Subject: encoded.Subject, RawMessageID: spec.id, ContentType: events.ContentType}
}

// acceptanceOwner 返回把组合交给另一个会话的前置操作；instance 为空表示只释放（会话关闭，没有新的持有者）。
// 这里只换归属、不动目标行（Trade 的释放流程还会删目标行、撤挂单，那属于前置步骤，不在这张表固定的范围里）：
// 所以旧会话的目标被拒绝之后，“当前目标”一列仍是旧会话留下的那一行。
func acceptanceOwner(instance, session string) func(*testing.T, *store.Store) {
	return func(t *testing.T, db *store.Store) { reassignLogicalOwner(t, db, instance, session) }
}

func acceptanceManual(t *testing.T, db *store.Store) {
	require.NoError(t, db.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'").Error)
}

func TestTargetAcceptanceTable(t *testing.T) {
	first := acceptanceStep{note: "第一条目标", target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.ACK, woke: true, current: "t1"}
	for _, test := range []struct {
		name  string
		steps []acceptanceStep
	}{
		{name: "同会话更新的目标取代旧目标", steps: []acceptanceStep{
			first,
			{note: "下一根的目标", target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.ACK, woke: true, current: "t2"},
		}},
		{name: "同会话更旧的目标被拒绝", steps: []acceptanceStep{
			{note: "较新一根的目标先到", target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.ACK, woke: true, current: "t2"},
			{note: "较旧一根的目标后到", target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.TERM, code: "superseded", current: "t2"},
		}},
		{name: "同会话同一根的另一个目标被拒绝", steps: []acceptanceStep{
			first,
			{note: "同一根、目标 ID 不同", target: acceptanceTarget{id: "t1b", weight: "2"}, decision: jetstream.TERM, code: "superseded", current: "t1"},
		}},
		{name: "同一目标重复投递", steps: []acceptanceStep{
			first,
			{note: "原样重投：确认，不再唤醒", target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.ACK, current: "t1"},
			{note: "已被下一根取代", target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.ACK, woke: true, current: "t2"},
			{note: "被取代之后重投旧目标：有回执，仍确认", target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.ACK, current: "t2"},
		}},
		{name: "同一目标 ID 内容不同", steps: []acceptanceStep{
			first,
			{note: "目标 ID 相同、权重不同", target: acceptanceTarget{id: "t1", weight: "3"}, decision: jetstream.TERM, code: "receipt_conflict", current: "t1"},
		}},
		{name: "换会话后旧会话的目标被拒绝", steps: []acceptanceStep{
			first,
			{note: "同一实例换了会话，旧会话更新一根的目标才到", before: acceptanceOwner("runner-1", "session-2"),
				target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.TERM, code: "superseded", current: "t1"},
		}},
		{name: "换会话后新会话的目标被接受", steps: []acceptanceStep{
			{note: "旧会话较新一根的目标", target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.ACK, woke: true, current: "t2"},
			// 新会话的目标不与旧会话的 bar_end_time 比较，直接覆盖旧会话留下的目标行。
			{note: "新会话的目标，bar_end_time 早于旧会话的目标", before: acceptanceOwner("runner-1", "session-2"),
				target: acceptanceTarget{id: "n1", session: "session-2", weight: "1"}, decision: jetstream.ACK, woke: true, current: "n1"},
			{note: "旧会话的目标重投", target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.TERM, code: "superseded", current: "n1"},
		}},
		{name: "会话关闭之后新到的旧会话目标被拒绝", steps: []acceptanceStep{
			first,
			{note: "会话关闭（没有新的持有者），旧会话下一根的目标才到", before: acceptanceOwner("", ""),
				target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.TERM, code: "superseded", current: "t1"},
		}},
		// 例外行：T14 起会话关闭不再删目标行、也不靠归属校验，这一行在 T14 里按新做法重写（结论仍是 superseded）。
		{name: "会话关闭之后的重复投递", steps: []acceptanceStep{
			first,
			{note: "会话关闭后重投已接受过的目标", before: acceptanceOwner("", ""),
				target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.TERM, code: "superseded", current: "t1"},
		}},
		{name: "组合被别的实例持有", steps: []acceptanceStep{
			{note: "别的实例的会话持有组合", before: acceptanceOwner("runner-2", "session-9"),
				target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.TERM, code: "authorization_conflict"},
			{note: "持有者自己的目标照常接受", target: acceptanceTarget{id: "o1", instance: "runner-2", session: "session-9", weight: "1"},
				decision: jetstream.ACK, woke: true, current: "o1"},
		}},
		{name: "人工控制的组合拒绝策略目标", steps: []acceptanceStep{
			first,
			{note: "改成人工控制后的新目标", before: acceptanceManual,
				target: acceptanceTarget{id: "t2", barMinutes: 1, weight: "2"}, decision: jetstream.TERM, code: "authorization_conflict", current: "t1"},
			{note: "人工控制下重投已接受过的目标", target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.TERM, code: "authorization_conflict", current: "t1"},
		}},
		{name: "过期的目标", steps: []acceptanceStep{
			{note: "有效期已过", target: acceptanceTarget{id: "t1", barMinutes: -120, validMinutes: -60, weight: "1"}, decision: jetstream.TERM, code: "target_expired"},
			{note: "过期先于其他判断：旧会话且过期，按过期终止", before: acceptanceOwner("runner-2", "session-9"),
				target: acceptanceTarget{id: "t2", barMinutes: -120, validMinutes: -60, weight: "1"}, decision: jetstream.TERM, code: "target_expired"},
		}},
		{name: "过期先于人工控制的判断", steps: []acceptanceStep{
			{note: "人工控制且过期，按过期终止", before: acceptanceManual,
				target: acceptanceTarget{id: "t1", barMinutes: -120, validMinutes: -60, weight: "1"}, decision: jetstream.TERM, code: "target_expired"},
		}},
		{name: "早于生效时刻送到的目标", steps: []acceptanceStep{
			{note: "bar_end_time 在未来、有效期内", target: acceptanceTarget{id: "t1", barMinutes: 5, weight: "1"}, decision: jetstream.ACK, woke: true, current: "t1"},
		}},
		{name: "暂停期间的目标留待重投", steps: []acceptanceStep{
			{note: "Trade 暂停", target: acceptanceTarget{id: "t1", weight: "1"}, intakeClosed: true, decision: jetstream.RETRY},
			{note: "恢复后重投", target: acceptanceTarget{id: "t1", weight: "1"}, decision: jetstream.ACK, woke: true, current: "t1"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openTargetStore(t)
			seedLogicalTargetAccount(t, db, true, true)
			now := time.Now().UTC().Truncate(time.Millisecond)
			for i, step := range test.steps {
				if step.before != nil {
					step.before(t, db)
				}
				woke := false
				opts := targetOptions(db, now)
				opts.WakeTarget = func(string, string) { woke = true }
				opts.Intake = func() bool { return !step.intakeClosed }
				resolved := false
				opts.WeightResolver = targetResolverFunc(func(ctx context.Context, request *tradeeventpb.LogicalAccountTargetWeightRequested, space string) (targetapp.WeightConversion, error) {
					resolved = true
					return testWeightResolver{}.Resolve(ctx, request, space)
				})
				result := HandleTarget(context.Background(), acceptanceDelivery(t, now, step.target), opts)
				require.Equal(t, step.decision, result.Decision, "第 %d 步（%s）的投递结论：%v", i, step.note, result.Err)
				code := ""
				if result.Decision == jetstream.TERM {
					code = targetErrorCode(result)
				}
				require.Equal(t, step.code, code, "第 %d 步（%s）的拒绝原因", i, step.note)
				require.Equal(t, step.woke, woke, "第 %d 步（%s）是否唤醒执行器", i, step.note)
				if !step.woke {
					require.False(t, resolved && result.Decision != jetstream.ACK, "第 %d 步（%s）：被拒绝或留待重投的目标不应进入换算", i, step.note)
				}
				current := ""
				if record, err := db.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1"); err == nil {
					current = record.TargetID
				}
				require.Equal(t, step.current, current, "第 %d 步（%s）之后组合上的当前目标", i, step.note)
			}
		})
	}
}
