package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func addInstrument(t *testing.T, tradeStore *store.Store, market, base string) {
	t.Helper()
	record := store.InstrumentRecord{
		Exchange: "BINANCE", MarketType: market, ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-" + market,
		BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
	}
	if market == "SWAP" {
		record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", base
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error { return tx.UpsertInstrument(record) }))
}

func newMixedResolver(t *testing.T, settings string) (*WeightResolver, *store.Store) {
	tradeStore := newMixedResolverStore(t, settings)
	return &WeightResolver{Store: tradeStore, Prices: targetPriceStub{price: shared.MustDecimal("100")}, Equity: conversionEquityStub{}, Now: func() time.Time { return time.UnixMilli(2000) }}, tradeStore
}

// 排除资产按标的表里的基础资产判断：底仓是 PEPE 时，1000PEPE 是另一个资产，不被排除。
func TestExcludedAssetUsesInstrumentBaseAssetNotNamePrefix(t *testing.T) {
	resolver, tradeStore := newMixedResolver(t, `{"arbitrage_base_assets":["PEPE"]}`)
	addInstrument(t, tradeStore, "SPOT", "PEPE")
	addInstrument(t, tradeStore, "SWAP", "1000PEPE")

	conversion, err := resolver.Resolve(context.Background(), 0, mixedRequest(
		weightTarget("PEPE-USDT-SPOT", "SPOT", "0.1"), weightTarget("1000PEPE-USDT-SWAP", "SWAP", "0.1"),
	), "space-1")

	require.NoError(t, err)
	require.True(t, quantityOf(t, conversion, "PEPE-USDT-SPOT").Hold)
	require.False(t, quantityOf(t, conversion, "1000PEPE-USDT-SWAP").Hold)
}

// 清仓（空目标）不依赖资金设置：设置解析失败也不能挡住清仓。
func TestEmptyTargetFlattenIgnoresBrokenFundSettings(t *testing.T) {
	resolver, _ := newMixedResolver(t, `{"reserve_ratio":"not-a-number"}`)

	conversion, err := resolver.Resolve(context.Background(), 0, mixedRequest(), "space-1")

	require.NoError(t, err)
	require.Empty(t, conversion.QuantityTargets)
}

// 只开合约的组合没有 BNB 现货可补买，不为 BNB 保留金额扣钱。
func TestBNBReserveIsNotDeductedWithoutSpotBNBInstrument(t *testing.T) {
	resolver, tradeStore := newMixedResolver(t, `{"bnb_reserve_amount":"500"}`)
	require.NoError(t, tradeStore.DBForTest().Exec(`DELETE FROM t_trade_instruments WHERE c_instrument_id = 'BNB-USDT-SPOT'`).Error)

	conversion, err := resolver.Resolve(context.Background(), 0, mixedRequest(weightTarget("BTC-USDT-SWAP", "SWAP", "0.5")), "space-1")

	require.NoError(t, err)
	require.Equal(t, "2000", conversion.Equity.String())
}

// BNB 市值要用新鲜报价，过期报价不能拿来算保留金额的差额。
func TestBNBMarketValueRequiresFreshPrice(t *testing.T) {
	resolver, _ := newMixedResolver(t, `{"bnb_reserve_amount":"500"}`)
	resolver.Prices = targetPriceStub{price: shared.MustDecimal("100")}
	resolver.Now = func() time.Time { return time.UnixMilli(2000).Add(time.Hour) } // 报价停在 2000，早已过期

	_, err := resolver.Resolve(context.Background(), 0, mixedRequest(weightTarget("BTC-USDT-SPOT", "SPOT", "0.5")), "space-1")

	require.Error(t, err)
}
