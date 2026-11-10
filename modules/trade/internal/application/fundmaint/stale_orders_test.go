package fundmaint

import (
	"context"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// seedSystemOrder 写入一笔指定状态的旧系统单。
func seedSystemOrder(t *testing.T, f *fixture, orderID, ownerID, instrument, symbol, state string, submittedAt int64) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-a", LogicalAccountID: "logical-1", ClientOrderID: orderID,
			Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: instrument, ExchangeSymbol: symbol,
			OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "200", LimitPrice: strPtr("0.9995"), ReferencePrice: "1",
			OwnerType: "SYSTEM", OwnerID: ownerID, State: state, SubmittedAt: submittedAt, Version: 1,
		})
	}))
}

// 提交失败停在待提交的补买单没有任何代码推进它：不丢弃的话补买一直“忙”，稳定币调平一直让路。
func TestBNBTopUpDiscardsAStalePendingOrderAndPlacesANewOne(t *testing.T) {
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	seedSystemOrder(t, f, "stale-pending", ownerBNBReserve, "BNB-USDT-SPOT", "BNBUSDT", "PENDING", 0)
	require.NoError(t, f.run())
	require.Equal(t, []string{"stale-pending"}, f.orders.discarded)
	require.Len(t, f.orders.specs, 1, "待提交的旧单丢弃后，同一轮下新的补买单")
	require.Equal(t, "BNB-USDT-SPOT", f.orders.specs[0].InstrumentID)
}

// 提交结果未知的补买单先解析：查到成挂单就继续等它，退回待提交的丢弃，本轮返回忙、下一轮再重下。
func TestBNBTopUpResolvesAnUnknownOrderBeforeDeciding(t *testing.T) {
	f := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	seedSystemOrder(t, f, "unknown-1", ownerBNBReserve, "BNB-USDT-SPOT", "BNBUSDT", "SUBMIT_UNKNOWN", 1_000)
	f.orders.resolveTo = orderdomain.Open
	require.NoError(t, f.run())
	require.Equal(t, []string{"unknown-1"}, f.orders.resolved)
	require.Empty(t, f.orders.discarded)
	require.Empty(t, f.orders.specs, "解析成挂单：补买单还在，不重复下")

	g := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	seedSystemOrder(t, g, "unknown-2", ownerBNBReserve, "BNB-USDT-SPOT", "BNBUSDT", "SUBMIT_UNKNOWN", 1_000)
	g.orders.resolveTo = orderdomain.Pending
	require.NoError(t, g.run())
	require.Equal(t, []string{"unknown-2"}, g.orders.resolved)
	require.Equal(t, []string{"unknown-2"}, g.orders.discarded, "查无此单退回待提交，接着丢弃")
	require.Empty(t, g.orders.specs, "解析过旧单的这一轮不下新单，下一轮按新快照再算缺口")

	h := newFixture(t, `{"bnb_reserve_amount":"200"}`, []store.AssetBalance{balance("USDT", "1000")})
	seedSystemOrder(t, h, "unknown-3", ownerBNBReserve, "BNB-USDT-SPOT", "BNBUSDT", "SUBMIT_UNKNOWN", 1_000)
	require.NoError(t, h.run(), "仍在查询窗口内：不报错，下一轮再解析")
	require.Empty(t, h.orders.specs)
}

// 稳定币调平遇到结果未知的旧买单：先解析，不直接撤单（域模型不接受对结果未知的单撤单），也不报错。
func TestStablecoinResolvesAnUnknownOldBuyBeforeCancelling(t *testing.T) {
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	seedSystemOrder(t, f, "unknown-buy", ownerStableRebalance, "USDC-USDT-SPOT", "USDCUSDT", "SUBMIT_UNKNOWN", testNow.Add(-56*time.Minute).UnixMilli())
	require.NoError(t, f.run())
	require.Equal(t, []string{"unknown-buy"}, f.orders.resolved)
	require.Empty(t, f.orders.canceled)
	require.Empty(t, f.orders.specs, "旧单还没结束的这一轮不下新单")

	// 解析成挂单：接着撤。
	g := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	seedSystemOrder(t, g, "unknown-buy", ownerStableRebalance, "USDC-USDT-SPOT", "USDCUSDT", "SUBMIT_UNKNOWN", testNow.Add(-56*time.Minute).UnixMilli())
	g.orders.resolveTo = orderdomain.Open
	require.NoError(t, g.run())
	require.Equal(t, []string{"unknown-buy"}, g.orders.canceled)

	// 解析后退回待提交：丢弃。
	h := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	seedSystemOrder(t, h, "unknown-buy", ownerStableRebalance, "USDC-USDT-SPOT", "USDCUSDT", "SUBMIT_UNKNOWN", testNow.Add(-56*time.Minute).UnixMilli())
	h.orders.resolveTo = orderdomain.Pending
	require.NoError(t, h.run())
	require.Equal(t, []string{"unknown-buy"}, h.orders.discarded)
	require.Empty(t, h.orders.canceled)
}

// 待提交的旧买单直接丢弃；撤单中的走撤单恢复。
func TestStablecoinDiscardsPendingAndRecoversCancelingOldBuys(t *testing.T) {
	f := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	seedSystemOrder(t, f, "pending-buy", ownerStableRebalance, "USDC-USDT-SPOT", "USDCUSDT", "PENDING", 0)
	require.NoError(t, f.run())
	require.Equal(t, []string{"pending-buy"}, f.orders.discarded)
	require.Empty(t, f.orders.canceled)

	g := newFixture(t, stableSettings, []store.AssetBalance{balance("USDT", "800"), balance("USDC", "200")})
	seedSystemOrder(t, g, "canceling-buy", ownerStableRebalance, "USDC-USDT-SPOT", "USDCUSDT", "CANCELING", testNow.Add(-56*time.Minute).UnixMilli())
	require.NoError(t, g.run())
	require.Equal(t, []string{"canceling-buy"}, g.orders.recovered)
	require.Empty(t, g.orders.canceled)
}
