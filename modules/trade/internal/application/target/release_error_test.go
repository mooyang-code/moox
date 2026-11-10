package target

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// requireReadableOnce 断言错误文本是中文、没有英文前缀，账户 ID 只出现一次（这段文本会原样展示给用户）。
func requireReadableOnce(t *testing.T, err error, accountID string) {
	t.Helper()
	require.NotContains(t, err.Error(), "target account")
	require.NotContains(t, err.Error(), "target execution")
	require.Equal(t, 1, strings.Count(err.Error(), accountID), err.Error())
	require.Contains(t, err.Error(), "目标单")
}

// ReleaseTargetOrders 对外（停用策略实例时展示给用户）只返回中文错误，不带账户错误的前缀。
func TestReleaseTargetOrdersErrorIsPlainChinese(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.gtcOrder(t, "cancel-unknown", "target-old", "CANCEL_UNKNOWN")
	err := f.restingExecutor().ReleaseTargetOrders(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	require.NotContains(t, err.Error(), "执行账户 account-a：")
	requireReadableOnce(t, err, "account-a")
}

// 目标 worker 只认 *AccountError（记成诊断、模块仍就绪），其余错误会把 target_worker_ready 置 false。
// 旧单撤单结果未知又确认不了时，收敛返回的必须是账户错误。
func TestStaleCancelUnknownFailureIsAccountError(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.gtcOrder(t, "stale-cancel-unknown", "target-old", "CANCEL_UNKNOWN")

	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.IsType(t, &AccountError{}, err, "worker 直接按类型判断")
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	requireReadableOnce(t, err, "account-a")
}

// 过期释放没解析出来、没撤下来时同样返回账户错误，并按 expiryRetryDelay 重试。
func TestExpiryReleaseFailureIsAccountError(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.gtcOrderBy(t, "cancel-unknown", "target-current", "CANCEL_UNKNOWN", "instance-1")

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.IsType(t, &AccountError{}, err)
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	requireReadableOnce(t, err, "account-a")
	require.Equal(t, expiryRetryDelay, result.RetryAfter)
}
