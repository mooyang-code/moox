package target

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 单笔上限拆单：目标 100 个（金额 10000），单笔上限 3000，每轮一片，持仓跟上后下一轮按最新持仓重算：30、30、30、10。
func TestSplitFollowsMaxChildNotionalAndRecomputesEveryBatch(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"3000"}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	position := shared.Zero()
	var slices []string
	for round := 0; round < 6; round++ {
		f.orders.specs = nil
		f.converge(t)
		if len(f.orders.specs) == 0 {
			break
		}
		slices = append(slices, f.orders.specs[0].Quantity.String())
		position = position.Add(f.orders.specs[0].Quantity)
		f.position(t, "account-a", "BTCUSDT", position.String())
	}
	require.Equal(t, []string{"30", "30", "30", "10"}, slices)
}

func TestSplitFractionIsReproducibleAndInRange(t *testing.T) {
	seen := make(map[string]struct{})
	for sent := 0; sent < 40; sent++ {
		seed := fmt.Sprintf("logical-1|target-1|BTC-USDT-SWAP|%d", sent)
		fraction := splitFraction(seed)
		require.Equal(t, fraction.String(), splitFraction(seed).String(), "同一个种子同一个比例")
		require.GreaterOrEqual(t, fraction.Cmp(shared.MustDecimal("0.5")), 0)
		require.LessOrEqual(t, fraction.Cmp(shared.MustDecimal("1")), 0)
		seen[fraction.String()] = struct{}{}
	}
	require.Greater(t, len(seen), 10, "不同的笔数得到不同的比例")
}

func TestRandomizedSliceStaysBetweenHalfAndFullLimit(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"4000","randomize_split":true}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1000"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	notional := f.orders.specs[0].Quantity.Mul(shared.MustDecimal("100"))
	require.GreaterOrEqual(t, notional.Cmp(shared.MustDecimal("2000")), 0, "不少于单笔上限的 50%%")
	require.LessOrEqual(t, notional.Cmp(shared.MustDecimal("4000")), 0)
	first := f.orders.specs[0].Quantity.String()

	// 同样的组合、目标、标的和笔数得到同样的数量（可复现）。
	again := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"4000","randomize_split":true}`)
	again.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1000"}})
	again.converge(t)
	require.Equal(t, first, again.orders.specs[0].Quantity.String())
}

// 批间隔取下单设置的值：第二轮在间隔内，不发新批，带回还要等的时间。
func TestBatchIntervalComesFromOrderSettings(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","batch_interval_seconds":5}`)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	executor := f.executor()
	first, err := executor.Converge(testContext(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "place", first.Action)
	f.orders.specs = nil
	second, err := executor.Converge(testContext(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "pace", second.Action)
	require.Equal(t, 5*time.Second, second.RetryAfter)
	require.Empty(t, f.orders.specs)
}
