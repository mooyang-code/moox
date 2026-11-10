package target

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 目标 worker 只认 *AccountError（记成诊断、模块仍就绪），其余错误会把 target_worker_ready 置 false。
// 旧单撤单结果未知又确认不了时，收敛返回的必须是账户错误。
func TestStaleCancelUnknownFailureIsAccountError(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.gtcOrder(t, "stale-cancel-unknown", "target-old", "CANCEL_UNKNOWN")

	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	var accountErr *AccountError
	require.True(t, errors.As(err, &accountErr))
	require.IsType(t, &AccountError{}, err, "worker 直接按类型判断")
	require.Equal(t, "account-a", err.(*AccountError).TradingAccountID)
	require.ErrorIs(t, err, errTargetOrderUnresolved)
}

// 过期释放没解析出来、没撤下来时同样返回账户错误，并按 expiryRetryDelay 重试。
func TestExpiryReleaseFailureIsAccountError(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.gtcOrderBy(t, "cancel-unknown", "target-current", "CANCEL_UNKNOWN", "instance-1")

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.IsType(t, &AccountError{}, err)
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	require.Equal(t, expiryRetryDelay, result.RetryAfter)
}
