package order

import (
	"context"
	"errors"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 目标单落库与提交时的会话检查结果表（批 3 T14 前置 R3）：用真实的下单服务和真实的 SQLite store，固定
// “目标单所属的目标还是不是当前会话的”时能不能落库、能不能发往交易所。守的是落库时 store 的归属检查
// （infra/store/fact.go 的 validateOrderOwnership）和提交前的授权检查（service.go 的 checkTargetAuthorization）；
// 执行器自己的检查在 target 包的 TestExecutorSessionTable。
//
// 表里只写“谁是当前会话”这个前提，由 orderCurrentSession 搭出来，不写它靠什么实现。T14 之前 Trade 靠组合的归属（owner）
// 判断当前会话；T14 起这两处检查改成与当前目标行比较，只换了 orderCurrentSession 的搭法，结论不变。

// orderCurrentSession 是这张表唯一的“谁是当前会话”前提：让 instanceID 的 sessionID 成为组合的当前会话，
// 并写入它的一份目标 targetID（取代组合上原来的目标行）。
//
// T14 之前的搭法：先释放组合原来的归属、改由这个会话认领，再写它的目标行。
// T14 起：Trade 不再看归属，只写一行开始时刻（session_started_at，按 orderSessionStartedAt 取）更晚的目标行，不改 owner。
func orderCurrentSession(t *testing.T, db *store.Store, instanceID, sessionID, targetID string) {
	t.Helper()
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		// 同一份目标重写时 accepted 为假（已经是当前目标），所以按写完后的当前目标判断。
		current, _, err := tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: targetID,
			InstanceID: instanceID, SessionID: sessionID, SessionStartedAt: orderSessionStartedAt(sessionID), StrategyID: "strategy-1",
			BarEndTime: 1, ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
			Status: "PENDING", AcceptedAt: 1,
			Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"}},
		})
		if err == nil && current.TargetID != targetID {
			err = errors.New("目标没有写成当前目标")
		}
		return err
	}))
}

// orderSessionStartedAt 是表里会话的开始时刻：session-1 < session-2 < session-9。
func orderSessionStartedAt(sessionID string) int64 {
	switch sessionID {
	case "session-2":
		return 2_000
	case "session-9":
		return 9_000
	default:
		return 1_000
	}
}

func TestTargetOrderSessionTable(t *testing.T) {
	type session struct{ instance, session string }
	for _, test := range []struct {
		name string
		// beforePlace、beforeSubmit 非空时，在落库前、提交前让这个会话成为当前会话（带它自己的一份目标）。
		beforePlace  *session
		beforeSubmit *session
		// placeRejected 表示落库被拒，之后不再提交。只断言被拒、不断言错误类型：store 的归属检查返回 ErrConflict，
		// Place 把 ErrConflict 当成同一客户端单号重复落库去查已有订单，查不到，最后返回的是 gorm.ErrRecordNotFound。
		placeRejected bool
		// submitErr 是提交返回的错误。
		submitErr error
		// sent 表示订单发往了交易所。
		sent bool
	}{
		{name: "目标仍属当前会话：落库并发往交易所", sent: true},
		{name: "落库前已被别的实例的新会话取代：不落单", beforePlace: &session{"runner-2", "session-9"}, placeRejected: true},
		{name: "提交前已被同一实例的新会话取代：不发单", beforeSubmit: &session{"runner-1", "session-2"}, submitErr: ErrTargetOwnerConflict},
		{name: "提交前已被别的实例的新会话取代：不发单", beforeSubmit: &session{"runner-2", "session-9"}, submitErr: ErrTargetOwnerConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, db, adapter := newTestService(t)
			// 夹具已让 runner-1 的 session-1 持有组合、带目标 owner-1；这里再走一遍前提函数，让表只依赖它。
			orderCurrentSession(t, db, "runner-1", "session-1", "owner-1")
			spec := testSpec(service.now())
			setTestOwner(&spec, orderdomain.OwnerTarget)
			if test.beforePlace != nil {
				orderCurrentSession(t, db, test.beforePlace.instance, test.beforePlace.session, "target-next")
			}

			pending, err := service.Place(context.Background(), "space-1", spec)

			if test.placeRejected {
				require.Error(t, err, "目标单落库应被拒")
				_, lookupErr := db.GetOrderByClientID(context.Background(), "space-1", "account-1", "SPOT", spec.ClientOrderID)
				require.ErrorIs(t, lookupErr, gorm.ErrRecordNotFound, "被拒的目标单不落库")
				require.Zero(t, adapter.placeCalls, "被拒的目标单不发往交易所")
				return
			}
			require.NoError(t, err)
			require.Equal(t, orderdomain.Pending, pending.State)
			if test.beforeSubmit != nil {
				orderCurrentSession(t, db, test.beforeSubmit.instance, test.beforeSubmit.session, "target-next")
			}

			submitted, err := service.Submit(context.Background(), "space-1", string(pending.ID))

			if test.submitErr != nil {
				require.ErrorIs(t, err, test.submitErr)
				require.Equal(t, orderdomain.Pending, submitted.State, "被拒的目标单留在待提交")
			} else {
				require.NoError(t, err)
			}
			if test.sent {
				require.Equal(t, 1, adapter.placeCalls, "目标单发往交易所")
			} else {
				require.Zero(t, adapter.placeCalls, "被拒的目标单不发往交易所")
			}
		})
	}
}
