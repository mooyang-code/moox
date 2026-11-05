package target

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// newMixedResolverStore 建一个同时开通现货和合约的组合：现货钱包里放着用户自己的 BTC 和 BNB。
func newMixedResolverStore(t *testing.T, fundSettings string) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", Name: "account-a", Exchange: "BINANCE",
			Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "PAPER", Environment: "PAPER",
			SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED", Ready: true,
			LeverageSettings: store.LeverageSettings{"*": "5"},
			Snapshot: store.TradingAccountSnapshot{
				AvailableFunds: "100000",
				Balances: []store.AssetBalance{
					{Wallet: "UNIFIED", Asset: "USDT", Available: "100000", Total: "100000"},
					{Wallet: "UNIFIED", Asset: "BTC", Available: "10", Total: "10"},
					{Wallet: "UNIFIED", Asset: "BNB", Available: "1", Total: "1"},
				},
			},
			LastSyncAt: 1_900, SnapshotSourceTime: 1_900,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
			FundSettingsJSON: fundSettings,
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		for _, instrument := range []struct{ market, base string }{
			{"SPOT", "BTC"}, {"SWAP", "BTC"}, {"SPOT", "BNB"}, {"SPOT", "ETH"}, {"SWAP", "ETH"},
		} {
			record := store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: instrument.market, ExchangeSymbol: instrument.base + "USDT",
				InstrumentID: instrument.base + "-USDT-" + instrument.market, BaseAsset: instrument.base, QuoteAsset: "USDT",
				SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}
			if instrument.market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", instrument.base
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	return tradeStore
}

func mixedRequest(targets ...*tradeeventpb.InstrumentWeightTarget) *tradeeventpb.LogicalAccountTargetWeightRequested {
	return &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "target-1", LogicalAccountId: "logical-1", InstanceId: "instance-1", SessionId: "session-1", StrategyId: "strategy-1",
		BarEndTime: timestamppb.New(time.UnixMilli(1000)), EffectiveAt: timestamppb.New(time.UnixMilli(1000)), ValidUntil: timestamppb.New(time.UnixMilli(61000)),
		Targets: targets,
	}
}

func weightTarget(instrumentID, market, weight string) *tradeeventpb.InstrumentWeightTarget {
	return &tradeeventpb.InstrumentWeightTarget{InstrumentId: instrumentID, MarketType: market, TargetWeight: weight}
}

func quantityOf(t *testing.T, conversion WeightConversion, instrumentID string) store.InstrumentTarget {
	t.Helper()
	for _, target := range conversion.QuantityTargets {
		if target.InstrumentID == instrumentID {
			return target
		}
	}
	t.Fatalf("换算结果里没有 %s", instrumentID)
	return store.InstrumentTarget{}
}

// 现货 BTC 余额只对现货标的有意义：目标里同时有 BTC 现货和 BTC 合约时，只按现货算一次，合约标的不会把它再加一遍。
func TestUnmanagedTargetValueCountsSpotBalanceOnceAndIgnoresSwap(t *testing.T) {
	tradeStore := newMixedResolverStore(t, "")
	resolver := WeightResolver{Store: tradeStore, Prices: targetPriceStub{price: shared.MustDecimal("100")}, Equity: conversionEquityStub{}, Now: func() time.Time { return time.UnixMilli(2000) }}

	both, err := resolver.Resolve(context.Background(), 0, mixedRequest(
		weightTarget("BTC-USDT-SPOT", "SPOT", "0.5"), weightTarget("BTC-USDT-SWAP", "SWAP", "-0.5"),
	), "space-1")
	require.NoError(t, err)
	require.Equal(t, "3000", both.Equity.String(), "权益 2000 加上 10 个现货 BTC 的市值 1000，只加一次")
	require.Equal(t, "15", quantityOf(t, both, "BTC-USDT-SPOT").Quantity)
	require.Equal(t, "-15", quantityOf(t, both, "BTC-USDT-SWAP").Quantity)

	swapOnly, err := resolver.Resolve(context.Background(), 0, mixedRequest(weightTarget("BTC-USDT-SWAP", "SWAP", "0.5")), "space-1")
	require.NoError(t, err)
	require.Equal(t, "2000", swapOnly.Equity.String(), "只有合约目标时，现货 BTC 不是目标资产，不计入换算权益")
	require.Equal(t, "10", quantityOf(t, swapOnly, "BTC-USDT-SWAP").Quantity)
}

// 排除资产只跳过这一项并按“保持”处理，其余目标照常换算；整份目标全被跳过也不会变成清仓。
func TestExcludedAssetsAreHeldAndOthersConvert(t *testing.T) {
	tradeStore := newMixedResolverStore(t, `{"bnb_reserve_amount":"100","arbitrage_base_assets":["ETH"]}`)
	resolver := WeightResolver{Store: tradeStore, Prices: targetPriceStub{price: shared.MustDecimal("100")}, Equity: conversionEquityStub{}, Now: func() time.Time { return time.UnixMilli(2000) }}

	conversion, err := resolver.Resolve(context.Background(), 0, mixedRequest(
		weightTarget("BNB-USDT-SPOT", "SPOT", "0.1"), weightTarget("ETH-USDT-SWAP", "SWAP", "-0.1"),
		weightTarget("ETH-USDT-SPOT", "SPOT", "0.1"), weightTarget("BTC-USDT-SPOT", "SPOT", "0.5"),
	), "space-1")
	require.NoError(t, err)
	for _, id := range []string{"BNB-USDT-SPOT", "ETH-USDT-SWAP", "ETH-USDT-SPOT"} {
		require.True(t, quantityOf(t, conversion, id).Hold, "%s 是排除资产，只保持不动", id)
	}
	require.False(t, quantityOf(t, conversion, "BTC-USDT-SPOT").Hold)
	require.Equal(t, "15", quantityOf(t, conversion, "BTC-USDT-SPOT").Quantity)

	onlyExcluded, err := resolver.Resolve(context.Background(), 0, mixedRequest(weightTarget("BNB-USDT-SPOT", "SPOT", "0.1")), "space-1")
	require.NoError(t, err)
	require.Len(t, onlyExcluded.QuantityTargets, 1)
	require.True(t, onlyExcluded.QuantityTargets[0].Hold, "全被跳过时仍是保持，不是空目标（清仓）")
}

// 同一个组合、同一个资产每天只告警一次。
func TestExcludedAssetWarningOncePerDay(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	resolver := WeightResolver{Now: func() time.Time { return now }}
	ctx := context.Background()

	require.True(t, resolver.warnExcluded(ctx, "logical-1", "BNB", "BNB-USDT-SPOT"))
	require.False(t, resolver.warnExcluded(ctx, "logical-1", "BNB", "BNB-USDT-SPOT"))
	require.True(t, resolver.warnExcluded(ctx, "logical-1", "ETH", "ETH-USDT-SPOT"), "换一个资产重新告警")
	require.True(t, resolver.warnExcluded(ctx, "logical-2", "BNB", "BNB-USDT-SPOT"), "换一个组合重新告警")
	now = now.Add(24 * time.Hour)
	require.True(t, resolver.warnExcluded(ctx, "logical-1", "BNB", "BNB-USDT-SPOT"), "第二天再告警一次")
}

// 换算权益 = 权益 + 未受管现货 - 权益 × 资金预留比例 - max(0, BNB 保留金额 - BNB 市值)。
func TestConversionEquityDeductsReserveRatioAndBNBShortfall(t *testing.T) {
	// 权益 2000 + 现货 BTC 1000 = 3000；预留 10% = 300；BNB 保留 500，现有 1 个 BNB = 100，还差 400。
	tradeStore := newMixedResolverStore(t, `{"reserve_ratio":"0.1","bnb_reserve_amount":"500"}`)
	resolver := WeightResolver{Store: tradeStore, Prices: targetPriceStub{price: shared.MustDecimal("100")}, Equity: conversionEquityStub{}, Now: func() time.Time { return time.UnixMilli(2000) }}

	conversion, err := resolver.Resolve(context.Background(), 0, mixedRequest(weightTarget("BTC-USDT-SPOT", "SPOT", "0.5")), "space-1")

	require.NoError(t, err)
	require.Equal(t, "2300", conversion.Equity.String())
	require.Equal(t, "11.5", quantityOf(t, conversion, "BTC-USDT-SPOT").Quantity)

	// BNB 买到位（市值不低于保留金额）后不再扣。
	enough := newMixedResolverStore(t, `{"bnb_reserve_amount":"100"}`)
	resolver.Store = enough
	conversion, err = resolver.Resolve(context.Background(), 0, mixedRequest(weightTarget("BTC-USDT-SPOT", "SPOT", "0.5")), "space-1")
	require.NoError(t, err)
	require.Equal(t, "3000", conversion.Equity.String())
}

// 做空校验按标的市场：同一份目标里现货不能为负，合约可以为负。
func TestShortingIsCheckedPerInstrumentMarket(t *testing.T) {
	tradeStore := newMixedResolverStore(t, "")
	resolver := WeightResolver{Store: tradeStore, Prices: targetPriceStub{price: shared.MustDecimal("100")}, Equity: conversionEquityStub{}, Now: func() time.Time { return time.UnixMilli(2000) }}

	_, err := resolver.Resolve(context.Background(), 0, mixedRequest(
		weightTarget("BTC-USDT-SWAP", "SWAP", "-0.2"), weightTarget("ETH-USDT-SPOT", "SPOT", "-0.1"),
	), "space-1")

	require.ErrorIs(t, err, ErrPermanent)
	require.Contains(t, err.Error(), "ETH-USDT-SPOT")
}
