package target

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 执行器下单前的会话检查结果表（批 3 T14 前置 R3）：固定“目标还属不属于当前会话”时执行器下不下单、返回什么。
// 表里只写“谁是当前会话”这个前提，由 becomeCurrentSession 搭出来，不写它靠什么实现。
// T14 之前 Trade 靠组合的归属（owner）判断当前会话；T14 起改成看当前目标行属于哪个会话，只换了 becomeCurrentSession
// 的搭法，表里每一行的结论不变。影子目标不进执行器，它的四行在 eventconsumer 的 TestShadowTargetSessionTable，
// 前提由那边的 shadowCurrentSession 搭，改法同理。这张表用下单服务桩，落库与提交时的检查（store 的 validateOrderOwnership、
// 下单服务的 checkTargetAuthorization）在 order 包的 TestTargetOrderSessionTable，用真实的下单服务和 SQLite。
//
// 没有“收敛开始之前就被新会话取代”这一行：T14 之后执行器读到的就是新会话的目标行，会照常为它下单，
// 结论和现在（按 owner 拒绝）不同，不是与机制无关的用例。

// becomeCurrentSession 是表里唯一的“谁是当前会话”前提：让 runner-1 的 sessionID 成为组合的当前会话，
// 并写入它的一份目标（BTC-USDT-SWAP 数量 1，bar_end_time 取测试时钟）。
//
// T14 之前的搭法：先释放组合原来的归属、改由这个会话认领，再写它的目标行。
// T14 起：Trade 不再看归属，只写一行开始时刻（session_started_at，按 sessionStartedAt 取）更晚的目标行，不改 owner。
//
// 收敛过程中也会调用（模拟报价、准备期间被新会话取代）：那时 Converge 持有组合锁，所以这里只用事务里的写法，
// 不调会再拿组合锁的 Store.AcceptLogicalAccountTarget。
func becomeCurrentSession(t *testing.T, f *targetFixture, sessionID, targetID string) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		_, accepted, err := tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: targetID,
			InstanceID: "runner-1", SessionID: sessionID, SessionStartedAt: sessionStartedAt(sessionID), StrategyID: "strategy-1",
			BarEndTime: f.now.UnixMilli(), ValidUntil: fixtureValidUntil(f.now),
			Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
			Status:  StatusPending, AcceptedAt: f.now.UnixMilli(),
		})
		if err == nil && !accepted {
			err = errors.New("目标没有写成当前目标")
		}
		return err
	}))
}

// sessionHookPriceSource 在第一次取报价时调用 hook：模拟报价期间组合状态被改。
type sessionHookPriceSource struct {
	targetPriceStub
	hook func()
}

func (s *sessionHookPriceSource) LatestPrice(ctx context.Context, accountID string, key exchange.SymbolKey) (Quote, error) {
	if hook := s.hook; hook != nil {
		s.hook = nil
		hook()
	}
	return s.targetPriceStub.LatestPrice(ctx, accountID, key)
}

func TestExecutorSessionTable(t *testing.T) {
	for _, test := range []struct {
		name string
		// arrange 在收敛前布置前提；默认只有 session-1 是当前会话、带一份目标。
		arrange func(t *testing.T, f *targetFixture, e *Executor)
		// err 是收敛返回的错误，nil 表示不报错。
		err error
		// status 是收敛结果的状态；err 非空时不看。
		status string
		// placed、submitted、discarded 分别是落库的待提交单数、发往交易所的单数、丢弃的待提交单。
		placed    int
		submitted int
		discarded []string
		// targetStatus 非空时断言收敛之后当前目标行的状态。
		targetStatus string
	}{
		{
			name:   "目标仍属当前会话：下单",
			status: StatusConverging, placed: 1, submitted: 1,
		},
		{
			name: "报价期间被新会话的目标取代：不下单",
			arrange: func(t *testing.T, f *targetFixture, e *Executor) {
				e.Prices = &sessionHookPriceSource{targetPriceStub: targetPriceStub{price: e.Prices.(targetPriceStub).price}, hook: func() {
					becomeCurrentSession(t, f, "session-2", "target-next")
				}}
			},
			err: ErrTargetSession,
		},
		{
			name: "落单之后、提交之前被新会话的目标取代：丢弃待提交单，不发往交易所",
			arrange: func(t *testing.T, f *targetFixture, _ *Executor) {
				f.orders.afterPlace = func() { becomeCurrentSession(t, f, "session-2", "target-next") }
			},
			err: ErrTargetSession, placed: 1, discarded: []string{"child-account-a"},
		},
		{
			name: "人工控制：不下单",
			arrange: func(t *testing.T, f *targetFixture, _ *Executor) {
				require.NoError(t, f.store.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'").Error)
			},
			err: ErrTargetSession,
		},
		{
			name: "目标过期：走过期路径，不下单",
			arrange: func(t *testing.T, f *targetFixture, _ *Executor) {
				target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
				require.NoError(t, err)
				f.now = time.UnixMilli(target.ValidUntil).UTC()
			},
			status: StatusExpired, targetStatus: StatusExpired,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newTargetFixture(t, exchange.MarketTypeSwap)
			becomeCurrentSession(t, f, "session-1", "target-current")
			e := f.executor()
			if test.arrange != nil {
				test.arrange(t, f, e)
			}

			result, err := e.Converge(context.Background(), "space-1", "logical-1")

			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.status, result.Status)
			}
			require.Len(t, f.orders.specs, test.placed, "落库的待提交单")
			require.Len(t, f.orders.submitted, test.submitted, "发往交易所的单")
			require.Equal(t, test.discarded, f.orders.discarded, "丢弃的待提交单")
			if test.targetStatus != "" {
				target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
				require.NoError(t, err)
				require.Equal(t, "target-current", target.TargetID)
				require.Equal(t, test.targetStatus, target.Status)
			}
		})
	}
}
