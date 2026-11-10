package accountsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/adaptertest"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// seedLocalOrder 写入一笔 moox 自己的合约订单（数量 1）。
func seedLocalOrder(t *testing.T, tradeStore *store.Store, orderID, state, exchangeOrderID, filled string) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-1", ClientOrderID: orderID + "-client",
			ExchangeOrderID: exchangeOrderID, Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTCUSDT",
			ExchangeSymbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY", PositionSide: "NET", Quantity: "1",
			ReferencePrice: "100", ReferencePriceAt: 1_000, OwnerType: "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", RunnerID: "runner-1", State: state,
			FilledQuantity: filled, ReservedAsset: "USDT", ReservedQuantity: "100", RemainingReservedQuantity: "100",
			SubmittedAt: 1_000, Version: 1,
		})
	}))
}

func claimFill(orderID, tradeID, quantity string, at int64) exchange.Fill {
	return exchange.Fill{
		Market: exchange.MarketTypeSwap, ExchangeTradeID: tradeID, ExchangeOrderID: "exchange-" + orderID,
		ClientOrderID: orderID + "-client", ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal(quantity), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(at),
	}
}

func claimService(tradeStore *store.Store, adapter *syncAdapter) *Service {
	return &Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
		Now:   func() time.Time { return time.UnixMilli(3_000) },
	}
}

func getOrder(t *testing.T, tradeStore *store.Store, orderID string) store.OrderRecord {
	t.Helper()
	record, err := tradeStore.GetOrder(context.Background(), "space-1", orderID)
	require.NoError(t, err)
	return record
}

// 退回待提交的订单，原来的请求其实成功了：REST 快照里带着它的成交，认领成挂单并入账，同步成功。
func TestRESTSnapshotFillClaimsPendingOrder(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedLocalOrder(t, tradeStore, "pending-rest", "PENDING", "", "0")
	service := claimService(tradeStore, &syncAdapter{})

	result, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
		Fills:   []exchange.Fill{claimFill("pending-rest", "trade-1", "0.4", 2_000)},
		Account: exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(2_500)},
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)
	record := getOrder(t, tradeStore, "pending-rest")
	require.Equal(t, "PARTIALLY_FILLED", record.State)
	require.Equal(t, "exchange-pending-rest", record.ExchangeOrderID)
	require.Equal(t, "0.4", record.FilledQuantity)
	require.Equal(t, uint64(2), record.Version, "一次入账只写一次库")
}

// 私有流的成交也走同一个 Reducer：认领后整单成交。
func TestPrivateStreamFillClaimsPendingOrder(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedLocalOrder(t, tradeStore, "pending-stream", "PENDING", "", "0")
	service := claimService(tradeStore, &syncAdapter{})

	applied, err := service.ApplyFill(context.Background(), "account-1", claimFill("pending-stream", "trade-1", "1", 2_000))
	require.NoError(t, err)
	require.True(t, applied)
	record := getOrder(t, tradeStore, "pending-stream")
	require.Equal(t, "FILLED", record.State)
	require.Equal(t, "exchange-pending-stream", record.ExchangeOrderID)
	require.Equal(t, "1", record.FilledQuantity)
	require.Equal(t, "0", record.RemainingReservedQuantity)
}

// 待提交的订单在快照里是已撤销：直接变已撤销，同步成功。
func TestSnapshotCanceledStatusFinishesPendingOrder(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedLocalOrder(t, tradeStore, "pending-canceled", "PENDING", "", "0")
	service := claimService(tradeStore, &syncAdapter{})

	_, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
		Orders: []exchange.Order{{
			Market: exchange.MarketTypeSwap, ExchangeOrderID: "exchange-pending-canceled", ClientOrderID: "pending-canceled-client",
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusCanceled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		}},
		Account: exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(2_500)},
	})
	require.NoError(t, err)
	record := getOrder(t, tradeStore, "pending-canceled")
	require.Equal(t, "CANCELED", record.State)
	require.Equal(t, "0", record.RemainingReservedQuantity)
	require.NotZero(t, record.FinishedAt)
}

// 待提交的订单在快照里已是部分成交、已成交：与挂单一样认领，先写交易所单号并转为挂单，不停在待提交。
func TestSnapshotFilledStatusClaimsPendingOrder(t *testing.T) {
	for _, status := range []exchange.OrderStatus{exchange.OrderStatusPartiallyFilled, exchange.OrderStatusFilled} {
		t.Run(string(status), func(t *testing.T) {
			tradeStore := openSyncStore(t)
			seedSyncAccount(t, tradeStore)
			seedLocalOrder(t, tradeStore, "pending-filled", "PENDING", "", "0")
			service := claimService(tradeStore, &syncAdapter{})

			_, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
				Orders: []exchange.Order{{
					Market: exchange.MarketTypeSwap, ExchangeOrderID: "exchange-pending-filled", ClientOrderID: "pending-filled-client",
					ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
					Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.4"), Status: status,
					CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
				}},
				Account: exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(2_500)},
			})
			require.NoError(t, err)
			record := getOrder(t, tradeStore, "pending-filled")
			require.Equal(t, "OPEN", record.State)
			require.Equal(t, "exchange-pending-filled", record.ExchangeOrderID)
		})
	}
}

// missingAdapter：交易所已经查不到这笔单（-2013），但近期成交里有它。
type missingAdapter struct {
	adaptertest.Stub
	fills []exchange.Fill
}

func (*missingAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (*missingAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds: shared.MustDecimal("900"), Equity: shared.MustDecimal("1000"), ExchangeUpdatedAt: time.UnixMilli(2_000),
	}, nil
}
func (*missingAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (*missingAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) { return nil, nil }
func (a *missingAdapter) ListRecentFills(_ context.Context, key exchange.SymbolKey, cursor string) ([]exchange.Fill, string, error) {
	if key.Symbol != "BTC-USDT" {
		return nil, cursor, nil
	}
	return a.fills, "99", nil
}
func (*missingAdapter) GetOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Code: "-2013"}
}
func (*missingAdapter) GetOrderByExchangeID(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Code: "-2013"}
}
func (*missingAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*missingAdapter) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}

// 交易所已不存在的订单，同一轮里先被成交入账成了已成交：随后的“按已撤销处理”不能报非法迁移，同步成功、订单保持已成交。
func TestMissingOrderWithFillsInTheSameRoundStaysFilled(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedLocalOrder(t, tradeStore, "missing-filled", "OPEN", "exchange-missing-filled", "0")
	adapter := &missingAdapter{fills: []exchange.Fill{claimFill("missing-filled", "trade-1", "1", 2_000)}}
	service := &Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true), Fills: &consumer.Reducer{Store: tradeStore},
		Now: func() time.Time { return time.Now().Add(10 * time.Minute) },
	}

	result, err := service.SyncAccountFull(context.Background(), "account-1")
	require.NoError(t, err)
	record := getOrder(t, tradeStore, "missing-filled")
	require.Equal(t, "FILLED", record.State)
	require.Equal(t, "1", record.FilledQuantity)
	require.NotContains(t, strings.Join(result.Warnings, "\n"), "按已撤销处理", "订单已是已成交，提示不能说已撤销")
	require.Contains(t, strings.Join(result.Warnings, "\n"), "当前状态 FILLED")
}

// 成交入账失败（这里是普通订单超量）：同步失败、账户未就绪，并有一条带订单号和原因的关注项；不隔离这一笔。
func TestFillIngestFailureLeavesAccountNotReadyWithAttention(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedLocalOrder(t, tradeStore, "filled-order", "FILLED", "exchange-filled-order", "1")
	service := claimService(tradeStore, &syncAdapter{})

	_, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
		Fills:   []exchange.Fill{claimFill("filled-order", "trade-over", "0.5", 2_000)},
		Account: exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(2_500)},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "filled-order-client")
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)

	notes := telemetry.Recent(telemetry.AttentionFillIngestFailed, time.UnixMilli(3_000), time.Hour)
	require.True(t, containsDetail(notes, "filled-order-client"), "关注项要带订单号：%v", notes)
	require.True(t, containsDetail(notes, "trade-over"), "关注项要带成交号：%v", notes)
	require.True(t, containsDetail(notes, "transition"), "关注项要带原因：%v", notes)
}

// B1(c)：交易所查不到原单时按成交合成外部订单。REST 一批只取本批成交之和，第二批到达时订单放大数量、保持已成交。
func TestRESTSyntheticExternalOrderGrowsWithLaterBatches(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{lookupErr: &exchange.Error{Kind: exchange.ErrorOrderNotFound}}
	service := claimService(tradeStore, adapter)
	fill := func(tradeID, quantity string, at int64) exchange.Fill {
		return exchange.Fill{
			Market: exchange.MarketTypeSwap, ExchangeTradeID: tradeID, ExchangeOrderID: "archived-order",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal(quantity), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(at),
		}
	}
	snapshotOf := func(fills ...exchange.Fill) Snapshot {
		return Snapshot{Fills: fills, Account: exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(2_500)}}
	}

	_, err := service.ApplySnapshot(context.Background(), "account-1", snapshotOf(fill("trade-1", "0.2", 1_000)))
	require.NoError(t, err)
	_, err = service.ApplySnapshot(context.Background(), "account-1", snapshotOf(fill("trade-2", "0.3", 1_100)))
	require.NoError(t, err, "第二批成交不能报非法迁移、卡住账户")
	_, err = service.ApplySnapshot(context.Background(), "account-1", snapshotOf(fill("trade-3", "0.1", 1_200), fill("trade-4", "0.1", 1_300)))
	require.NoError(t, err)

	record, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTC-USDT"}, "archived-order")
	require.NoError(t, err)
	require.Contains(t, record.OrderID, "synthetic:")
	require.Equal(t, "0.7", record.Quantity)
	require.Equal(t, "0.7", record.FilledQuantity)
	require.Equal(t, "FILLED", record.State)
}

// B1(c) 私有流路径：每笔成交各取本笔，第二笔到达时同样放大。
func TestPrivateStreamSyntheticExternalOrderGrowsWithLaterFills(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{lookupErr: &exchange.Error{Kind: exchange.ErrorOrderNotFound}}
	service := claimService(tradeStore, adapter)
	for index, quantity := range []string{"0.2", "0.3"} {
		applied, err := service.ApplyFill(context.Background(), "account-1", exchange.Fill{
			Market: exchange.MarketTypeSwap, ExchangeTradeID: "stream-trade-" + quantity, ExchangeOrderID: "archived-stream-order",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal(quantity), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_000 + int64(index)),
		})
		require.NoError(t, err, "第 %d 笔", index+1)
		require.True(t, applied)
	}
	record, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTC-USDT"}, "archived-stream-order")
	require.NoError(t, err)
	require.Equal(t, "0.5", record.Quantity)
	require.Equal(t, "0.5", record.FilledQuantity)
	require.Equal(t, "FILLED", record.State)
}

// 能查到原单的外部订单数量是交易所给的，不是合成的：超量仍然是非法迁移。
func TestQueriedExternalOrderDoesNotGrow(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{order: exchange.Order{
		Market: exchange.MarketTypeSwap, ExchangeOrderID: "real-external", ExchangeSymbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.2"), FilledQuantity: shared.MustDecimal("0.2"), Status: exchange.OrderStatusFilled,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_500),
	}}
	service := claimService(tradeStore, adapter)
	fill := func(tradeID, quantity string, at int64) exchange.Fill {
		return exchange.Fill{
			Market: exchange.MarketTypeSwap, ExchangeTradeID: tradeID, ExchangeOrderID: "real-external",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal(quantity), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(at),
		}
	}
	applied, err := service.ApplyFill(context.Background(), "account-1", fill("real-1", "0.2", 1_400))
	require.NoError(t, err)
	require.True(t, applied)
	_, err = service.ApplyFill(context.Background(), "account-1", fill("real-2", "0.3", 1_500))
	require.Error(t, err)
}
