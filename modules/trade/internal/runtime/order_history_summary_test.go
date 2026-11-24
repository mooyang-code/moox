package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// referenceAverageCost 是改用标的汇总之前的均价算法（读这个标的全部成交按时间重放），留作对照。
func referenceAverageCost(fills []store.FillRecord) shared.Decimal {
	sort.SliceStable(fills, func(i, j int) bool { return fills[i].TradedAt < fills[j].TradedAt })
	quantity, cost := shared.Zero(), shared.Zero()
	for _, fill := range fills {
		fillQuantity, quantityErr := shared.ParseDecimal(fill.Quantity)
		fillPrice, priceErr := shared.ParseDecimal(fill.Price)
		if quantityErr != nil || priceErr != nil || fillQuantity.IsZero() {
			continue
		}
		if strings.EqualFold(fill.Side, "BUY") {
			quantity, cost = quantity.Add(fillQuantity), cost.Add(fillQuantity.Mul(fillPrice))
			continue
		}
		if !strings.EqualFold(fill.Side, "SELL") || quantity.IsZero() {
			continue
		}
		reduce := fillQuantity
		if reduce.Cmp(quantity) > 0 {
			reduce = quantity
		}
		average := cost.Div(quantity)
		quantity, cost = quantity.Sub(reduce), cost.Sub(average.Mul(reduce))
	}
	if quantity.IsZero() {
		return shared.Zero()
	}
	return cost.Div(quantity)
}

// referenceManagedInstruments 是改用标的汇总之前的受管判断：有成交的策略或人工订单涉及的标的。
const referenceManagedInstruments = `SELECT DISTINCT c_instrument_id FROM t_trade_orders
	WHERE c_space_id = ? AND c_trading_account_id = ?
		AND c_owner_type IN ('TARGET', 'OPERATOR') AND c_filled_quantity != '0' AND c_instrument_id != ''`

type summaryFill struct {
	base, owner, side, price, quantity string
}

// 模拟现货账户的均价、最近成交价、受管资产：改前（从成交历史现算）与改后（读标的汇总）逐账户一致；
// 把保留天数调短真删订单与成交之后，持仓均价、最近成交价（权益取不到价时的回退）、受管判断和模拟余额都不变。
func TestInstrumentSummaryMatchesFillHistoryAndSurvivesPruning(t *testing.T) {
	ctx := context.Background()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	accounts := map[string][]summaryFill{
		// 卖出超过持有量、零仓时卖出、清仓归零后再买，来源混着 TARGET、OPERATOR、EXTERNAL。
		"paper-a": {
			{"BTC", "TARGET", "BUY", "100", "2"}, {"BTC", "EXTERNAL", "SELL", "110", "3"}, {"BTC", "EXTERNAL", "SELL", "105", "1"},
			{"BTC", "OPERATOR", "BUY", "120", "1"}, {"BTC", "EXTERNAL", "BUY", "90", "3"}, {"BTC", "TARGET", "SELL", "100", "2"},
			{"ETH", "EXTERNAL", "BUY", "10", "4"}, {"ETH", "EXTERNAL", "SELL", "12", "1"},
		},
		// 只有系统单和外部单：都不受管。
		"paper-b": {
			{"BTC", "SYSTEM", "BUY", "50", "0.5"}, {"ETH", "EXTERNAL", "BUY", "20", "2"}, {"ETH", "OPERATOR", "BUY", "30", "2"},
		},
	}
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		for _, base := range []string{"BTC", "ETH"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: base + "USDT", InstrumentID: base + "-USDT-SPOT",
				BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		for id, fills := range accounts {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: id, Name: id, Exchange: "BINANCE", Markets: []string{"SPOT"},
				ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
			}); err != nil {
				return err
			}
			if err := tx.SetPaperOrderRetentionDays("space-1", id, 1); err != nil {
				return err
			}
			if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{SpaceID: "space-1", LogicalAccountID: "logical-" + id, Name: id,
				ExecutionMode: "PAPER", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test"}); err != nil {
				return err
			}
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{SpaceID: "space-1", LogicalAccountID: "logical-" + id,
				TradingAccountID: id, Enabled: true, Priority: 1}); err != nil {
				return err
			}
			// TARGET 单要挂在当前目标的实例下；目标里不列标的，受管只来自成交。
			if _, _, err := tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-" + id, TargetID: "target-" + id,
				InstanceID: "runner-1", SessionID: "session-1", SessionStartedAt: 1_000, StrategyID: "strategy-1",
				BarEndTime: time.Now().Add(-time.Minute).UnixMilli(), ValidUntil: time.Now().Add(2 * time.Hour).UnixMilli(),
				Targets: []store.InstrumentTarget{}, Status: "PENDING", AcceptedAt: time.Now().UnixMilli(),
			}); err != nil {
				return err
			}
			for i, fill := range fills {
				orderID := fmt.Sprintf("%s-%d", id, i)
				instanceID, ownerID := "", orderID
				if fill.owner == "TARGET" {
					instanceID, ownerID = "runner-1", "target-"+id
				}
				if err := tx.CreateOrder(store.OrderRecord{
					LogicalAccountID: "logical-" + id, InstanceID: instanceID,
					SpaceID: "space-1", OrderID: orderID, TradingAccountID: id, ClientOrderID: "client-" + orderID,
					Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: fill.base + "-USDT-SPOT", ExchangeSymbol: fill.base + "USDT",
					OrderType: "MARKET", Side: fill.side, Quantity: fill.quantity, ReferencePrice: fill.price,
					OwnerType: fill.owner, OwnerID: ownerID, State: "FILLED", Version: 1, FinishedAt: old.Add(time.Duration(i) * time.Minute).UnixMilli(),
				}); err != nil {
					return err
				}
				if _, err := tx.InsertFill(store.FillRecord{
					SpaceID: "space-1", FillID: orderID, ExchangeTradeID: orderID, OrderID: orderID,
					Side: fill.side, Price: fill.price, Quantity: fill.quantity, Fee: "0", FeeAsset: "USDT",
					SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: old.Add(time.Duration(i) * time.Minute).UnixMilli(),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}))
	// 订单上的已成交量照实写上（这里的订单是直接建成 FILLED 的），旧的受管判断看它。
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_trade_orders SET c_filled_quantity = c_quantity`).Error)

	type snapshot struct {
		summaries map[string]store.InstrumentSummaryRecord
		managed   map[string]struct{}
		balances  map[string]string
	}
	take := func(id string) snapshot {
		result := snapshot{summaries: map[string]store.InstrumentSummaryRecord{}, balances: map[string]string{}}
		for _, base := range []string{"BTC", "ETH"} {
			summary, found, err := tradeStore.GetInstrumentSummary(ctx, "space-1", id, exchange.NewSymbolKey(exchange.MarketTypeSpot, base+"USDT"))
			require.NoError(t, err)
			require.True(t, found, id+" "+base)
			result.summaries[base] = summary
		}
		managed, err := tradeStore.ManagedAssets(ctx, "space-1", id)
		require.NoError(t, err)
		result.managed = managed
		balances, err := tradeStore.GetPaperBalanceSnapshot(ctx, "space-1", id)
		require.NoError(t, err)
		for asset, total := range balances.Totals {
			result.balances[asset] = total.String()
		}
		return result
	}
	before := map[string]snapshot{}
	for id := range accounts {
		current := take(id)
		before[id] = current
		// 改前改后一致：均价与按全部成交重放的结果相同，最近成交价是最晚一笔成交的价格。
		for _, base := range []string{"BTC", "ETH"} {
			fills, _, err := tradeStore.ListFills(ctx, "space-1", store.FillQuery{TradingAccountID: id, Market: "SPOT", ExchangeSymbol: base + "USDT", Limit: 100})
			require.NoError(t, err)
			require.NotEmpty(t, fills)
			summary := current.summaries[base]
			// ListFills 按成交时间倒序，第一笔是最晚的；referenceAverageCost 会原地排序，放在后面。
			require.Equal(t, fills[0].Price, summary.LastPrice.String(), id+" "+base)
			require.Equal(t, referenceAverageCost(fills).String(), summary.AverageCost().String(), id+" "+base)
		}
		// 受管资产与旧的按订单推导一致。
		var instrumentIDs []string
		require.NoError(t, tradeStore.DBForTest().Raw(referenceManagedInstruments, "space-1", id).Scan(&instrumentIDs).Error)
		want := map[string]struct{}{"USDT": {}}
		for _, instrumentID := range instrumentIDs {
			want[strings.SplitN(instrumentID, "-", 2)[0]] = struct{}{}
		}
		require.Equal(t, want, current.managed, id)
	}
	require.Equal(t, map[string]struct{}{"USDT": {}, "BTC": {}}, before["paper-a"].managed)
	require.Equal(t, map[string]struct{}{"USDT": {}, "ETH": {}}, before["paper-b"].managed)

	// 同一笔成交重放（私有流与 REST 各来一次）不重复累计。
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		inserted, err := tx.InsertFill(store.FillRecord{
			SpaceID: "space-1", FillID: "paper-a-0", ExchangeTradeID: "paper-a-0", OrderID: "paper-a-0",
			Side: "BUY", Price: "100", Quantity: "2", Fee: "0", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: old.UnixMilli(),
		})
		require.False(t, inserted)
		return err
	}))
	require.Equal(t, before["paper-a"], take("paper-a"))

	// 保留 1 天，10 天前结束的订单与成交全部真删，包括全部 TARGET、OPERATOR 订单。
	pruner := &OrderHistoryPruner{Store: tradeStore, Now: func() time.Time { return now }, batch: 2}
	require.NoError(t, pruner.RunAll(ctx))
	for id := range accounts {
		_, total, err := tradeStore.ListOrders(ctx, "space-1", store.OrderQuery{TradingAccountID: id})
		require.NoError(t, err)
		require.Zero(t, total, id)
		_, fills, err := tradeStore.ListFills(ctx, "space-1", store.FillQuery{TradingAccountID: id, Limit: 10})
		require.NoError(t, err)
		require.Zero(t, fills, id)
		require.Equal(t, before[id], take(id), id)
	}
}
