package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"testing"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// recordingReleaser 记下撤目标单的调用，并在调用时查看目标是否还在（释放会话时目标要先删掉，执行器不再按它下新单）。
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

// 释放会话：先删目标，再撤目标单（此时目标已经不在），最后释放会话。
func TestReleaseSessionDeletesTargetBeforeReleasingTargetOrders(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	releaser := &recordingReleaser{store: tradeStore}
	service.TargetOrders = releaser

	require.NoError(t, service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence))
	require.Equal(t, 1, releaser.calls)
	require.Equal(t, []bool{false}, releaser.targetPresent, "撤单时目标已删，执行器不会再下新单")
	_, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 撤单失败：不释放会话（目标已删，执行器不再下新单），返回的错误不会被当成“所有权冲突”或“账户不存在”
// （策略据此稍后重试释放）；重试成功后释放。
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
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "目标已删，执行器不会再按它下新单")

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

// 凭证失效、账户没有会话、标的已不存在时撤单永远不会成功：比照移出成员只按本地记录放行，释放会话并记关注项。
// 掺着别的错误（例如交易所暂时不可用）时照样不释放，让策略重试。
func TestReleaseSessionPassesWhenCredentialsUnavailable(t *testing.T) {
	noSession := &orderapp.AccountExecutionError{TradingAccountID: "account-1", Operation: "adapter", Err: errors.New("交易账户会话还没有启动，请稍后重试")}
	authFailed := &orderapp.AccountExecutionError{TradingAccountID: "account-1", Operation: "cancel", Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	for _, tc := range []struct {
		name     string
		err      error
		released bool
	}{
		{"没有会话", fmt.Errorf("目标单 o1：%w", noSession), true},
		{"凭证失效", errors.Join(fmt.Errorf("目标单 o1：%w", authFailed), fmt.Errorf("目标单 o2：%w", noSession)), true},
		{"标的已不存在", fmt.Errorf("目标单 o1：%w", &orderapp.AccountExecutionError{TradingAccountID: "account-1", Operation: "cancel",
			Err: &exchange.Error{Kind: exchange.ErrorRejected, Code: exchange.CodeBinanceInvalidSymbol}}), true},
		{"掺着暂时性错误", errors.Join(fmt.Errorf("目标单 o1：%w", authFailed), errors.New("目标单 o2：交易所暂时不可用")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, tradeStore, fence := modernSessionFixture(t)
			acceptModernSessionTarget(t, tradeStore, "session-1")
			service.TargetOrders = &recordingReleaser{store: tradeStore, err: tc.err}

			err := service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence)
			account, getErr := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, getErr)
			if tc.released {
				require.NoError(t, err)
				require.Empty(t, account.OwnerSessionID)
				return
			}
			require.ErrorIs(t, err, ErrTargetOrdersNotReleased)
			require.Equal(t, "session-1", account.OwnerSessionID)
		})
	}
}
