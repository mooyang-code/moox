package target

import (
	"context"
	"errors"
	"testing"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// requireReadable 断言错误文本是中文、没有英文前缀（这段文本会原样展示给用户）。
func requireReadable(t *testing.T, err error) {
	t.Helper()
	require.NotContains(t, err.Error(), "target account")
	require.NotContains(t, err.Error(), "target execution")
	require.Contains(t, err.Error(), "目标单")
}

// ReleaseTargetOrders 对外（停用策略实例时展示给用户）只返回中文错误，不带账户错误的前缀。
func TestReleaseTargetOrdersErrorIsPlainChinese(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.gtcOrder(t, "cancel-unknown", "target-old", "CANCEL_UNKNOWN")
	err := f.restingExecutor().ReleaseTargetOrders(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	require.NotContains(t, err.Error(), "执行账户")
	requireReadable(t, err)
}

// 撤旧单时交易所报错：账户 ID 只由外层“执行账户 X：”前缀给出一次，逐笔文本不再重复。
func TestStaleCancelExchangeErrorShowsAccountOnce(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.gtcOrder(t, "stale-open", "target-old", "OPEN")
	f.orders.cancelErr = &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "cancel", Err: errors.New("交易所超时")}

	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.IsType(t, &AccountError{}, err)
	require.Equal(t, "执行账户 account-a：目标单 stale-open：交易所超时", err.Error())
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
	requireReadable(t, err)
}

// 过期释放没解析出来、没撤下来时同样返回账户错误，并按 expiryRetryDelay 重试。
func TestExpiryReleaseFailureIsAccountError(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.gtcOrderBy(t, "cancel-unknown", "target-current", "CANCEL_UNKNOWN", "instance-1")

	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.IsType(t, &AccountError{}, err)
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	requireReadable(t, err)
	require.Equal(t, expiryRetryDelay, result.RetryAfter)
}
