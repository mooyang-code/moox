package test

import (
	"context"
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func limitSpec(clientID string, side exchange.Side, quantity, limit string, policy exchange.FillPolicy) orderdomain.OrderSpec {
	spec := marketSpec(clientID, side, quantity)
	price := shared.MustDecimal(limit)
	spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, policy, &price
	return spec
}

// 模拟盘的伪市价单（限价 + IOC）：价格在限价之内就按当时的可成交价（买一取卖一价）成交，不是按限价；
// 限价压得比可成交价还低就整笔撤掉，成交量为 0。
func TestPaperPseudoMarketIOCHonoursTheLimitPrice(t *testing.T) {
	ctx := context.Background()
	paper := newProductionPaperFixture(t, exchange.MarketTypeSpot)

	filled := mustPlace(t, paper, limitSpec("ioc-wide", exchange.SideBuy, "0.01", "51000", exchange.FillPolicyIOC))
	_, err := paper.orders.Submit(ctx, testSpace, string(filled.ID))
	require.NoError(t, err)
	tight := mustPlace(t, paper, limitSpec("ioc-tight", exchange.SideBuy, "0.01", "49000", exchange.FillPolicyIOC))
	_, err = paper.orders.Submit(ctx, testSpace, string(tight.ID))
	require.NoError(t, err)
	productionPaperMatch(t, paper)

	filled, err = paper.orders.Get(ctx, testSpace, string(filled.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, filled.State)
	require.Equal(t, "50000", filled.AverageFillPrice.String(), "按可成交价 50000 成交，不是按限价 51000")

	tight, err = paper.orders.Get(ctx, testSpace, string(tight.ID))
	require.NoError(t, err)
	require.True(t, tight.FilledQuantity.IsZero(), "限价 49000 低于可成交价，零成交")
	require.True(t, tight.State.Terminal(), "IOC 没成交的部分直接结束：%s", tight.State)
	require.NotEqual(t, orderdomain.Filled, tight.State)
}

// 模拟盘的挂单：限价 + GTC 挂在可成交价之下就留在簿上，行情下探到限价后再成交（算挂单方）。
func TestPaperRestingOrderWaitsForTheMarketToCrossTheLimit(t *testing.T) {
	ctx := context.Background()
	paper := newProductionPaperFixture(t, exchange.MarketTypeSpot)

	resting := mustPlace(t, paper, limitSpec("gtc-resting", exchange.SideBuy, "0.01", "49000", exchange.FillPolicyGTC))
	_, err := paper.orders.Submit(ctx, testSpace, string(resting.ID))
	require.NoError(t, err)
	productionPaperMatch(t, paper)
	resting, err = paper.orders.Get(ctx, testSpace, string(resting.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, resting.State, "49000 低于 50000，挂在簿上")
	require.True(t, resting.FilledQuantity.IsZero())

	// 行情下探到 48900，穿过限价：挂单成交，按当时的可成交价、算挂单方。
	paper.fake.mu.Lock()
	paper.fake.reference.Price = shared.MustDecimal("48900")
	paper.fake.mu.Unlock()
	productionPaperMatch(t, paper)
	resting, err = paper.orders.Get(ctx, testSpace, string(resting.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, resting.State)
	require.Equal(t, "48900", resting.AverageFillPrice.String())
}
