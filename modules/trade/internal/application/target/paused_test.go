package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// 组合暂停自动执行但仍绑着策略时，策略照常发目标。不管目标有效、已过期还是未生效，收敛都不能返回错误
// （返回错误会被计为一次“交易调仓”运行失败），也不能拉行情、下单。
func TestPausedLogicalAccountConvergesWithoutError(t *testing.T) {
	for name, tc := range map[string]struct {
		now    int64
		status string
	}{
		"有效目标":  {now: 1500, status: StatusPaused},
		"已过期目标": {now: 2000, status: StatusExpired},
		"未生效目标": {now: 999, status: StatusPaused},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTargetFixture(t, exchange.MarketTypeSwap)
			modernExpiryTarget(t, f)
			require.NoError(t, f.store.DBForTest().Exec(`UPDATE t_logical_accounts
				SET c_automation_state = 'PAUSED', c_pause_reason = '手动暂停', c_pause_source = 'USER'
				WHERE c_logical_account_id = 'logical-1'`).Error)
			f.position(t, "account-a", "BTCUSDT", "1")
			e := f.executor()
			e.Prices = expiryPriceSource{t}
			e.Now = func() time.Time { return time.UnixMilli(tc.now) }
			// 定时器每秒都会来，重复收敛的结论不变。
			for round := 0; round < 3; round++ {
				result, err := e.Converge(context.Background(), "space-1", "logical-1")
				require.NoError(t, err)
				require.Equal(t, tc.status, result.Status)
			}
			require.Empty(t, f.orders.specs)
			require.Empty(t, f.orders.submitted)
		})
	}
}
