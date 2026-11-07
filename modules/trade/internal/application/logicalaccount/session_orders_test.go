package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// recordingReleaser 记下撤目标单的调用，并在调用时查看目标是否还在（撤单必须在删目标之前）。
type recordingReleaser struct {
	store         *store.Store
	err           error
	calls         int
	targetPresent []bool
}

func (r *recordingReleaser) ReleaseTargetOrders(ctx context.Context, spaceID, logicalAccountID string) error {
	r.calls++
	_, err := r.store.GetLogicalAccountTarget(ctx, spaceID, logicalAccountID)
	r.targetPresent = append(r.targetPresent, err == nil)
	return r.err
}

// 释放会话：先撤目标挂单（此时目标还在），再释放会话、删掉目标。
func TestReleaseSessionReleasesTargetOrdersBeforeDeletingTarget(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	releaser := &recordingReleaser{store: tradeStore}
	service.TargetOrders = releaser

	require.NoError(t, service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence))
	require.Equal(t, 1, releaser.calls)
	require.Equal(t, []bool{true}, releaser.targetPresent)
	_, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 撤单失败：不释放、不删目标，返回的错误不会被当成“所有权冲突”或“账户不存在”（策略据此稍后重试释放）；重试成功后释放。
func TestReleaseSessionKeepsSessionWhenTargetOrdersRemain(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	releaser := &recordingReleaser{store: tradeStore, err: fmt.Errorf("cancel: %w", store.ErrConflict)}
	service.TargetOrders = releaser

	err := service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence)
	require.ErrorIs(t, err, ErrTargetOrdersNotReleased)
	require.False(t, errors.Is(err, store.ErrConflict), "不能映射成冲突，否则策略当成已了结不再重试")
	require.False(t, errors.Is(err, gorm.ErrRecordNotFound))
	account, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "session-1", account.OwnerSessionID)
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err, "目标还在，执行器照常管着它的挂单")

	releaser.err = nil
	require.NoError(t, service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence))
	account, err = tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, account.OwnerSessionID)
}

// 不会换掉所有者的请求不撤单：过期围栏的释放、同一会话重复认领、已经重绑过的重复重绑。
func TestSessionNoOpsDoNotReleaseTargetOrders(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	releaser := &recordingReleaser{store: tradeStore}
	service.TargetOrders = releaser
	ctx := context.Background()

	require.Error(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", "stale-fence"))
	_, _, err := service.ClaimSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence)
	require.NoError(t, err)
	_, _, err = service.ClaimSession(ctx, "space-1", "logical-1", "instance-2", "session-9", fence)
	require.Error(t, err, "被别的会话占用")
	require.Zero(t, releaser.calls)

	_, rebound, err := service.RebindSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence, "instance-1", "session-2")
	require.NoError(t, err)
	require.Equal(t, 1, releaser.calls, "真的重绑要撤旧会话的挂单")
	_, _, err = service.RebindSession(ctx, "space-1", "logical-1", "instance-1", "session-1", rebound, "instance-1", "session-2")
	require.NoError(t, err)
	require.Equal(t, 1, releaser.calls, "重复的重绑不撤新会话的挂单")
}

// 认领空闲的组合要先撤上一个会话可能留下的挂单；撤不下来也照常认领（新目标的第 1 阶段会再撤）。
func TestClaimSessionReleasesLeftoverTargetOrdersBestEffort(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	ctx := context.Background()
	require.NoError(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence))
	account, err := tradeStore.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	releaser := &recordingReleaser{store: tradeStore, err: errors.New("交易所暂时不可用")}
	service.TargetOrders = releaser

	claimed, _, err := service.ClaimSession(ctx, "space-1", "logical-1", "instance-2", "session-2", account.AuthFence)
	require.NoError(t, err)
	require.Equal(t, 1, releaser.calls)
	require.Equal(t, "session-2", claimed.OwnerSessionID)
}
