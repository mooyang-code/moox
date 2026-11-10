package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 每小时上限汇总时，已提交子单的金额解析不了：返回错误、本轮不下单，不能按 0 计把上限静默放宽。
func TestHourlyCapFailsWhenSubmittedNotionalCannotBeParsed(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	hour := f.now.Truncate(time.Hour)
	f.submittedChild(t, "used", "FILLED", "9", "9", "100", hour.Add(time.Second))
	require.NoError(t, f.store.DBForTest().Exec(
		`UPDATE t_trade_orders SET c_reference_price = 'bad', c_average_price = '' WHERE c_order_id = 'used'`).Error)

	executor := f.executor()
	executor.MinBatchInterval = 0
	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.ErrorContains(t, err, "无法解析")
	require.Empty(t, f.orders.specs)

	_, parseErr := submittedNotional(store.SubmittedTargetOrder{
		InstrumentID: "BTC-USDT-SWAP", State: "OPEN", Quantity: "oops", ReferencePrice: "100",
	})
	require.Error(t, parseErr)
	notional, err := submittedNotional(store.SubmittedTargetOrder{State: "OPEN", Quantity: "2", ReferencePrice: "100"})
	require.NoError(t, err)
	require.Equal(t, shared.MustDecimal("200").String(), notional.String())
}
