package consumer

import (
	"context"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 模拟盘资金费按成交推结算时刻的持仓，前提是持仓表只由撮合成交投影：随机买卖之后，
// 当前持仓减去全部成交应当回到零，任一时刻推出的持仓都等于那一刻之前成交的累计。
func TestPaperSwapPositionEqualsCumulativeFills(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSwap)
	reducer := &Reducer{Store: s}
	source := Source{SpaceID: testSpace, TradingAccountID: testAccount, Kind: OriginPaperMatcher}
	random := rand.New(rand.NewSource(7))
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	running := shared.Zero()
	cumulative := map[int64]shared.Decimal{}
	for i := 0; i < 40; i++ {
		side := exchange.SideBuy
		if random.Intn(2) == 0 {
			side = exchange.SideSell
		}
		quantity := shared.MustDecimal(strconv.Itoa(1+random.Intn(5)) + ".5")
		id := strconv.Itoa(i)
		require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
			return tx.CreateOrder(store.OrderRecord{
				SpaceID: testSpace, OrderID: "order-" + id, TradingAccountID: testAccount, ClientOrderID: "client-" + id,
				ExchangeOrderID: "exchange-" + id, Exchange: string(exchange.ExchangeBinance), MarketType: string(exchange.MarketTypeSwap),
				ExchangeSymbol: testSymbol, OrderType: string(exchange.OrderTypeMarket), Side: string(side), PositionSide: string(exchange.PositionSideNet),
				Quantity: quantity.String(), ReferencePrice: "100", ReferencePriceAt: base.UnixMilli(), OwnerType: "EXTERNAL", OwnerID: "exchange-" + id,
				State: string(order.Open), ReservedAsset: "USDT", ReservedQuantity: "100", RemainingReservedQuantity: "100", Version: 1,
			})
		}))
		tradedAt := base.Add(time.Duration(i) * time.Minute)
		applied, err := reducer.ApplyFill(context.Background(), exchange.Fill{
			Market: exchange.MarketTypeSwap, ExchangeTradeID: "trade-" + id, ExchangeOrderID: "exchange-" + id, ClientOrderID: "client-" + id,
			ExchangeSymbol: testSymbol, Side: side, PositionSide: exchange.PositionSideNet, Quantity: quantity,
			Price: shared.MustDecimal(strconv.Itoa(90 + random.Intn(20))), SettlementAsset: "USDT", TradedAt: tradedAt,
		}, source)
		require.NoError(t, err)
		require.True(t, applied)
		cumulative[tradedAt.UnixMilli()] = running
		if side == exchange.SideBuy {
			running = running.Add(quantity)
		} else {
			running = running.Sub(quantity)
		}
	}
	activity, err := s.PaperSwapActivity(context.Background(), testSpace, testAccount, base.UnixMilli())
	require.NoError(t, err)
	require.Equal(t, running.String(), activity.Positions[testSymbol].String(), "持仓表等于成交累计")
	sweep := activity.Sweep()
	for i := 0; i < 40; i++ {
		at := base.Add(time.Duration(i) * time.Minute).UnixMilli()
		sweep.AdvanceTo(at)
		require.Equal(t, cumulative[at].String(), sweep.Quantity(testSymbol).String(), "第 %d 笔成交时刻之前的持仓", i)
	}
}
