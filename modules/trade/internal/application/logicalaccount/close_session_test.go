package logicalaccount

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// CloseTargetSession（批 3 的 3.6）：在组合锁内推进 c_closed_through、缩短这个会话的目标的有效期，提交之后唤醒一次执行器。
func TestCloseTargetSessionAdvancesShortensAndWakesAfterCommit(t *testing.T) {
	service, tradeStore, _ := modernSessionFixture(t)
	target := acceptModernSessionTarget(t, tradeStore, "session-1")
	now := time.Now().UTC().Add(time.Second)
	service.Now = func() time.Time { return now }
	type wake struct {
		space, logical string
		closed         int64
		validUntil     int64
	}
	var wakes []wake
	service.WakeTarget = func(spaceID, logicalAccountID string) {
		// 唤醒时关闭已经提交：执行器读得到推进后的 c_closed_through 和缩短后的有效期。
		account, err := tradeStore.GetLogicalAccount(context.Background(), spaceID, logicalAccountID)
		require.NoError(t, err)
		current, err := tradeStore.GetLogicalAccountTarget(context.Background(), spaceID, logicalAccountID)
		require.NoError(t, err)
		wakes = append(wakes, wake{space: spaceID, logical: logicalAccountID, closed: account.ClosedThrough, validUntil: current.ValidUntil})
	}

	closed, err := service.CloseTargetSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", target.SessionStartedAt)

	require.NoError(t, err)
	require.Equal(t, target.SessionStartedAt, closed)
	require.Equal(t, []wake{{space: "space-1", logical: "logical-1", closed: target.SessionStartedAt, validUntil: now.UnixMilli()}}, wakes)
	current, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err, "关闭不删目标行")
	require.Equal(t, target.TargetID, current.TargetID)
	require.Equal(t, target.Status, current.Status, "置为过期由执行器的过期路径做")
	require.Equal(t, now.UnixMilli(), current.ValidUntil)
}

// 幂等、不回退：重复关闭同一个会话结果不变（照样唤醒一次）；关闭更早的会话不让 c_closed_through 往回走。
func TestCloseTargetSessionIsIdempotentAndNeverMovesBack(t *testing.T) {
	service, tradeStore, _ := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	wakes := 0
	service.WakeTarget = func(string, string) { wakes++ }
	ctx := context.Background()

	for range 2 {
		closed, err := service.CloseTargetSession(ctx, "space-1", "logical-1", "instance-1", "session-2", 2_000)
		require.NoError(t, err)
		require.Equal(t, int64(2_000), closed)
	}
	closed, err := service.CloseTargetSession(ctx, "space-1", "logical-1", "instance-1", "session-1", 1_000)
	require.NoError(t, err)
	require.Equal(t, int64(2_000), closed, "关闭更早的会话不回退")
	account, err := tradeStore.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, int64(2_000), account.ClosedThrough)
	require.Equal(t, 3, wakes, "每次调用提交之后都唤醒一次")
}

// 没有目标行时也能关闭（会话从没发出过目标）；组合不存在时报不存在、不唤醒。
func TestCloseTargetSessionWithoutTargetAndUnknownAccount(t *testing.T) {
	service, tradeStore, _ := modernSessionFixture(t)
	wakes := 0
	service.WakeTarget = func(string, string) { wakes++ }
	ctx := context.Background()

	closed, err := service.CloseTargetSession(ctx, "space-1", "logical-1", "instance-1", "session-1", 1_000)
	require.NoError(t, err)
	require.Equal(t, int64(1_000), closed)
	_, err = tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Equal(t, 1, wakes)

	_, err = service.CloseTargetSession(ctx, "space-1", "missing", "instance-1", "session-1", 1_000)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = service.CloseTargetSession(ctx, "space-1", "logical-1", "instance-1", "session-1", 0)
	require.ErrorIs(t, err, store.ErrInvalidRecord)
	require.Equal(t, 1, wakes, "失败的关闭不唤醒")
}
