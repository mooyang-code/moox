package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
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

// 不会换掉所有者的请求不撤单：过期围栏的释放、同一会话重复认领、被别的会话占用时的认领。
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

// 凭证失效、交易账户已停用（没有会话）、标的已不存在时撤单永远不会成功：比照移出成员只按本地记录放行，释放会话并记关注项。
// 没有会话但交易账户仍启用（例如重启后会话还没建好）只是暂时的，不放行；掺着别的错误（例如交易所暂时不可用）时同样不放行，让策略重试。
func TestReleaseSessionPassesWhenCredentialsUnavailable(t *testing.T) {
	noSession := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "adapter", Err: errors.New("交易账户会话还没有启动，请稍后重试")}
	authFailed := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "cancel", Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	for _, tc := range []struct {
		name     string
		err      error
		disabled bool
		released bool
	}{
		{"没有会话、账户仍启用", fmt.Errorf("目标单 o1：%w", noSession), false, false},
		{"没有会话、账户已停用", fmt.Errorf("目标单 o1：%w", noSession), true, true},
		{"凭证失效", fmt.Errorf("目标单 o1：%w", authFailed), false, true},
		{"凭证失效且另一笔没有会话、账户仍启用", errors.Join(fmt.Errorf("目标单 o1：%w", authFailed), fmt.Errorf("目标单 o2：%w", noSession)), false, false},
		{"凭证失效且另一笔没有会话、账户已停用", errors.Join(fmt.Errorf("目标单 o1：%w", authFailed), fmt.Errorf("目标单 o2：%w", noSession)), true, true},
		{"标的已不存在", fmt.Errorf("目标单 o1：%w", &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "cancel",
			Err: &exchange.Error{Kind: exchange.ErrorRejected, Code: exchange.CodeBinanceInvalidSymbol}}), false, true},
		{"掺着暂时性错误", errors.Join(fmt.Errorf("目标单 o1：%w", authFailed), errors.New("目标单 o2：交易所暂时不可用")), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, tradeStore, fence := modernSessionFixture(t)
			acceptModernSessionTarget(t, tradeStore, "session-1")
			if tc.disabled {
				require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_trading_accounts SET c_status = 'DISABLED' WHERE c_trading_account_id = 'account-a'`).Error)
			}
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

// 交易账户处于异常（ERROR）状态、取不到会话：和已停用一样撤单永远不会成功，按本地记录放行并记关注项。
func TestReleaseSessionPassesWhenAccountInError(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_trading_accounts SET c_status = 'ERROR' WHERE c_trading_account_id = 'account-a'`).Error)
	noSession := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "adapter", Err: errors.New("交易账户会话还没有启动，请稍后重试")}
	service.TargetOrders = &recordingReleaser{store: tradeStore, err: fmt.Errorf("目标单 o1：%w", noSession)}
	telemetry.Forget(telemetry.AttentionReleaseOrdersUnverified, releaseUnverifiedDetail("logical-1"))

	require.NoError(t, service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence))
	account, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, account.OwnerSessionID)
	require.True(t, hasNote(telemetry.AttentionReleaseOrdersUnverified, "logical-1"))
}

// 交易账户启用、会话一直建不起来：释放照样返回“没撤下来”让策略重试；连续失败满 10 分钟记关注项 release_orders_stuck，
// 之后释放成功就清掉。
func TestReleaseSessionNotesStuckReleaseAfterTenMinutes(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }
	noSession := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "adapter", Err: errors.New("交易账户会话还没有启动，请稍后重试")}
	releaser := &recordingReleaser{store: tradeStore, err: fmt.Errorf("目标单 o1：%w", noSession)}
	service.TargetOrders = releaser
	release := func() error {
		return service.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence)
	}
	stuck := func() bool { return hasNote(telemetry.AttentionReleaseOrdersStuck, "logical-1") }
	telemetry.Forget(telemetry.AttentionReleaseOrdersStuck, releaseStuckDetail("logical-1"))

	require.ErrorIs(t, release(), ErrTargetOrdersNotReleased)
	now = now.Add(9 * time.Minute)
	require.ErrorIs(t, release(), ErrTargetOrdersNotReleased)
	require.False(t, stuck(), "不到 10 分钟不记")
	now = now.Add(time.Minute)
	require.ErrorIs(t, release(), ErrTargetOrdersNotReleased, "记了关注项也照样返回错误，让策略重试")
	require.True(t, stuck(), "连续失败满 10 分钟要记")
	account, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "session-1", account.OwnerSessionID)

	releaser.err = nil
	require.NoError(t, release())
	require.False(t, stuck(), "释放成功后清掉")
}

// 释放失败的计时只算连续失败：中间释放撤单成功就清掉；两次失败隔了超过 10 分钟也重新计时，
// 不会因为很久以前的一次失败，下一次失败一来就报 release_orders_stuck。
func TestReleaseStuckTimerRestartsAfterRecoveryOrLongGap(t *testing.T) {
	noSession := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "adapter", Err: errors.New("交易账户会话还没有启动，请稍后重试")}
	setup := func(t *testing.T) (*Service, *recordingReleaser, *time.Time, string) {
		service, tradeStore, fence := modernSessionFixture(t)
		acceptModernSessionTarget(t, tradeStore, "session-1")
		now := time.Now().UTC()
		service.Now = func() time.Time { return now }
		releaser := &recordingReleaser{store: tradeStore, err: fmt.Errorf("目标单 o1：%w", noSession)}
		service.TargetOrders = releaser
		telemetry.Forget(telemetry.AttentionReleaseOrdersStuck, releaseStuckDetail("logical-1"))
		return service, releaser, &now, fence
	}
	stuck := func(now time.Time) bool {
		for _, detail := range telemetry.Recent(telemetry.AttentionReleaseOrdersStuck, now, time.Hour) {
			if strings.Contains(detail, "logical-1") {
				return true
			}
		}
		return false
	}
	ctx := context.Background()

	t.Run("释放撤单成功后重新计时", func(t *testing.T) {
		service, releaser, now, fence := setup(t)
		start := *now
		require.ErrorIs(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence), ErrTargetOrdersNotReleased)
		*now = start.Add(5 * time.Minute)
		require.ErrorIs(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence), ErrTargetOrdersNotReleased)

		*now = start.Add(6 * time.Minute)
		releaser.err = nil
		require.NoError(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence))
		require.Equal(t, 3, releaser.calls, "释放真的撤了单")
		released, err := service.Store.GetLogicalAccount(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		_, claimed, err := service.ClaimSession(ctx, "space-1", "logical-1", "instance-1", "session-2", released.AuthFence)
		require.NoError(t, err)

		// 距第一次失败已满 10 分钟，但中间释放撤单成功过，这是新会话的第一次失败。
		*now = start.Add(11 * time.Minute)
		releaser.err = fmt.Errorf("目标单 o1：%w", noSession)
		require.ErrorIs(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-2", claimed), ErrTargetOrdersNotReleased)
		require.False(t, stuck(*now), "释放成功后要重新计时")
	})

	t.Run("两次失败隔了很久重新计时", func(t *testing.T) {
		service, _, now, fence := setup(t)
		start := *now
		require.ErrorIs(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence), ErrTargetOrdersNotReleased)
		*now = start.Add(24 * time.Hour)
		require.ErrorIs(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence), ErrTargetOrdersNotReleased)
		require.False(t, stuck(*now), "隔了一天的失败不算连续失败，不立刻报")
		*now = start.Add(24*time.Hour + 10*time.Minute)
		require.ErrorIs(t, service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence), ErrTargetOrdersNotReleased)
		require.True(t, stuck(*now), "从新的起点连续失败满 10 分钟照样要报")
	})
}

func hasNote(kind, needle string) bool {
	for _, detail := range telemetry.Recent(kind, time.Now(), time.Hour) {
		if strings.Contains(detail, needle) {
			return true
		}
	}
	return false
}
