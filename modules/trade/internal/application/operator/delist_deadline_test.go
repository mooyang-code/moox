package operator

import (
	"context"
	"testing"
	"time"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/stretchr/testify/require"
)

// 下架清仓带截止时间（下架时间 T）：过了 T 不再下单，动作记 PARTIAL；之后工作线程恢复（同一个动作号）也不再下单。
func TestDelistFlattenStopsAtTheDeadline(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-deadline", "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(-time.Second).UnixMilli()

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs, "过了下架时间不再下单")
	require.Equal(t, string(operatordomain.StatusPartial), result.Action.Status)
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-deadline")
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusPartial), stored.Status)
	decoded, err := decodeFlattenRequest(stored)
	require.NoError(t, err)
	require.Equal(t, command.DeadlineAt, decoded.DeadlineAt, "截止时间写进请求，重启恢复后仍生效")
}

// 截止时间还没到：照常下单。
func TestDelistFlattenBeforeTheDeadlineStillTrades(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-before-deadline", "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(time.Hour).UnixMilli()

	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
}
