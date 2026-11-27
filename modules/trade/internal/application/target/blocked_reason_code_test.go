package target

import (
	"context"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// TestBlockedTargetReasonCodesPopulatedAcrossAllMainPaths 验证执行器全部主要阻断路径
// 均输出准确且非空的结构化 BlockedTarget.ReasonCode，避免空码回退。
func TestBlockedTargetReasonCodesPopulatedAcrossAllMainPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("无对应可交易标的的受管现货与永续持仓记INSTRUMENT_UNAVAILABLE", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		// 写入两个同 BaseAsset="ORPHAN" 的现货标的使 spotInstrumentID 找不到唯一标的，
		// 并写入无对应可交易标的的永续持仓 GHOSTUSDT。
		f.setSpotBalance("ORPHAN", "5")
		f.setSwapPosition("GHOSTUSDT", "1")
		require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
			for _, sym := range []struct{ symbol, id string }{
				{"ORPHANUSDT", "ORPHAN-USDT-SPOT"},
				{"ORPHAN2USDT", "ORPHAN2-USDT-SPOT"},
			} {
				if err := tx.UpsertInstrument(store.InstrumentRecord{
					Exchange: "binance", MarketType: "spot", ExchangeSymbol: sym.symbol,
					InstrumentID: sym.id, BaseAsset: "ORPHAN", QuoteAsset: "USDT", SettlementAsset: "USDT",
					ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
				}); err != nil {
					return err
				}
			}
			return nil
		}))
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "ORPHAN-USDT-SPOT", Quantity: "0"},
		})

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		byID := map[string]store.BlockedTarget{}
		for _, item := range stored.BlockedTargets {
			require.NotEmpty(t, item.ReasonCode, "每条阻断项都必须带非空 ReasonCode: %+v", item)
			byID[item.InstrumentID] = item
		}
		require.Equal(t, store.BlockedReasonInstrumentUnavailable, byID["ORPHAN"].ReasonCode)
		require.Equal(t, store.BlockedReasonInstrumentUnavailable, byID["GHOSTUSDT"].ReasonCode)
	})

	t.Run("目标中无成员支持的标的通过laneAction记INSTRUMENT_UNAVAILABLE", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "UNKNOWN-USDT-SWAP", Quantity: "1"},
		})

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, stored.BlockedTargets, 1)
		require.Equal(t, "UNKNOWN-USDT-SWAP", stored.BlockedTargets[0].InstrumentID)
		require.Equal(t, store.BlockedReasonInstrumentUnavailable, stored.BlockedTargets[0].ReasonCode)
	})

	t.Run("资金容量不足记INSUFFICIENT_FUNDS", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		f.orders.capacities = map[string]shared.Decimal{"account-a": shared.Zero()}
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"},
		})

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, stored.BlockedTargets, 1)
		require.Equal(t, store.BlockedReasonInsufficientFunds, stored.BlockedTargets[0].ReasonCode)
	})

	t.Run("下单校验返回ErrInsufficientFunds记INSUFFICIENT_FUNDS", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		f.orders.placeErrors = map[string]error{"account-a": orderapp.ErrInsufficientFunds}
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"},
		})

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, stored.BlockedTargets, 1)
		require.Equal(t, store.BlockedReasonInsufficientFunds, stored.BlockedTargets[0].ReasonCode)
	})

	t.Run("同目标交易所拒单排除记EXCHANGE_REJECTED", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"},
		})
		targetRec, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
			return tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", OrderID: "ord-rej", TradingAccountID: "account-a", ClientOrderID: "c-rej",
				Exchange: "binance", MarketType: "swap", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
				OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", ReferencePriceAt: f.now.UnixMilli(),
				SubmittedAt: f.now.UnixMilli(),
				OwnerType:   "TARGET", OwnerID: targetRec.TargetID, LogicalAccountID: "logical-1", InstanceID: targetRec.InstanceID,
				State: "REJECTED", RejectCode: "EXCHANGE_REJECTED", RejectReason: "margin insufficient", Version: 1,
			})
		}))

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, stored.BlockedTargets, 1)
		require.Equal(t, store.BlockedReasonExchangeRejected, stored.BlockedTargets[0].ReasonCode)
	})

	t.Run("数量低于交易所最小值记ORDER_RULE", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.0001"},
		})

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, stored.BlockedTargets, 1)
		require.Equal(t, store.BlockedReasonOrderRule, stored.BlockedTargets[0].ReasonCode)
	})

	t.Run("同目标触发反向熔断记REVERSAL", func(t *testing.T) {
		f := newMixedExecutorFixture(t)
		f.now = time.Now().UTC()
		f.target(t, []store.InstrumentQuantity{
			{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"},
			{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"},
		})
		targetRec, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		// 写入 BUY -> SELL 两笔已成交子单，再次 BUY 即触发第 2 次反向熔断。
		require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
			for idx, side := range []string{"BUY", "SELL"} {
				if err := tx.CreateOrder(store.OrderRecord{
					SpaceID: "space-1", OrderID: "rev-" + side, TradingAccountID: "account-a", ClientOrderID: "c-rev-" + side,
					Exchange: "binance", MarketType: "swap", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
					OrderType: "MARKET", Side: side, Quantity: "1", FilledQuantity: "1",
					ReferencePrice: "100", ReferencePriceAt: f.now.UnixMilli(),
					SubmittedAt: f.now.UnixMilli() + int64(idx+1),
					OwnerType:   "TARGET", OwnerID: targetRec.TargetID, LogicalAccountID: "logical-1", InstanceID: targetRec.InstanceID,
					State: "FILLED", Version: 1,
				}); err != nil {
					return err
				}
			}
			return nil
		}))

		result, err := f.executor().Converge(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)

		stored, err := f.store.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, stored.BlockedTargets, 1)
		require.Equal(t, store.BlockedReasonReversal, stored.BlockedTargets[0].ReasonCode)
	})

	t.Run("scaleBuys在仅有原因文案时自动补齐非空ReasonCode", func(t *testing.T) {
		action := laneAction{
			member:     memberState{account: store.TradingAccountRecord{TradingAccountID: "account-a"}},
			instrument: store.InstrumentRecord{InstrumentID: "BTC-USDT-SPOT"},
			delta:      shared.MustDecimal("1"),
		}
		scaled := scaleBuys(
			[]laneAction{action},
			map[string]shared.Decimal{"account-a": shared.MustDecimal("0.5")},
			map[string]string{"account-a": "自定义划转失败"},
			nil,
		)
		require.Len(t, scaled, 1)
		require.Equal(t, store.BlockedReasonTransferFailed, scaled[0].scaleReasonCode)
	})
}
