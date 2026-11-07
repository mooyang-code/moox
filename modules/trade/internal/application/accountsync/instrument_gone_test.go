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

// delistedSymbol 是测试里已下架的合约：查单、查成交都回 -1121，交易所上也已经没有它的持仓。
const delistedSymbol = "DEAD-USDT"

// delistedAdapter 模拟标的已下架的交易所：涉及 delistedSymbol 的查询一律回“标的不存在”，其余标的正常、没有成交和挂单。
type delistedAdapter struct {
	adaptertest.Stub
	fillQueries []string
}

func instrumentGone() error {
	return &exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400, Code: exchange.CodeBinanceInvalidSymbol}
}

func (*delistedAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (*delistedAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds: shared.MustDecimal("900"), Equity: shared.MustDecimal("1000"),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
	}, nil
}
func (*delistedAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (*delistedAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) { return nil, nil }
func (a *delistedAdapter) ListRecentFills(_ context.Context, key exchange.SymbolKey, cursor string) ([]exchange.Fill, string, error) {
	a.fillQueries = append(a.fillQueries, key.Symbol)
	if key.Symbol == delistedSymbol {
		return nil, cursor, instrumentGone()
	}
	return nil, cursor, nil
}
func (*delistedAdapter) GetOrder(_ context.Context, key exchange.SymbolKey, _ string) (exchange.Order, error) {
	if key.Symbol == delistedSymbol {
		return exchange.Order{}, instrumentGone()
	}
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Code: "-2013"}
}
func (a *delistedAdapter) GetOrderByExchangeID(ctx context.Context, key exchange.SymbolKey, _ string) (exchange.Order, error) {
	return a.GetOrder(ctx, key, "")
}
func (*delistedAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*delistedAdapter) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}

// seedDelisted 在 account-1 上登记已下架的合约、它的成交游标和本地还没删掉的持仓，并放几笔各种状态的未结束订单。
func seedDelisted(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SWAP", ExchangeSymbol: delistedSymbol,
			InstrumentID: "DEADUSDT", BaseAsset: "DEAD", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "1",
			ContractValueAsset: "DEAD", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP", ExchangeSymbol: delistedSymbol,
			PositionSide: "NET", SignedQuantity: "2", EntryPrice: "1", MarkPrice: "1", Leverage: "5",
			MarginMode: "CROSS", ExchangeUpdatedAt: 1_000,
		}); err != nil {
			return err
		}
		order := func(id, state, exchangeOrderID string) store.OrderRecord {
			return store.OrderRecord{
				SpaceID: "space-1", OrderID: id, TradingAccountID: "account-1", ClientOrderID: id + "-client",
				ExchangeOrderID: exchangeOrderID, MarketType: "SWAP", ExchangeSymbol: delistedSymbol, OrderType: "LIMIT",
				TimeInForce: "IOC", Side: "SELL", PositionSide: "NET", Quantity: "2", ReferencePrice: "1", ReferencePriceAt: 1_000,
				ReduceOnly: true, OwnerType: "OPERATOR", OwnerID: "delist-1", LogicalAccountID: "logical-1",
				State: state, FilledQuantity: "0", ReservedAsset: "USDT", ReservedQuantity: "5", RemainingReservedQuantity: "5",
				SubmittedAt: time.Now().UnixMilli(), Version: 1,
			}
		}
		for _, record := range []store.OrderRecord{
			order("gone-unknown", "SUBMIT_UNKNOWN", ""),
			order("gone-open", "OPEN", "7"),
			order("gone-canceling", "CANCELING", "8"),
			order("gone-pending", "PENDING", ""),
			order("gone-submitting", "SUBMITTING", ""),
		} {
			limit := "1"
			record.LimitPrice = &limit
			if err := tx.CreateOrder(record); err != nil {
				return err
			}
		}
		return nil
	}))
	account, err := tradeStore.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	cursors := account.FillCursors
	cursors["SWAP:"+delistedSymbol] = "41"
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountFacts("space-1", "account-1", cursors, account.Snapshot, 1, 1)
	}))
}

// 下架清仓最后一笔 IOC 结果未知、标的随后下架：查单、查成交回 -1121 不能让整次同步失败（否则账户一直未就绪，
// 清仓、减仓、目标执行全被挡住）。发出过的单按过期结束、写明需人工核对、释放资金预留并记关注项；
// 待提交、刚提交中的单不动；成交游标保留；持仓以交易所为准（交易所已经没有这笔持仓）。
func TestSyncClosesOrdersOfDelistedInstrument(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedDelisted(t, tradeStore)
	adapter := &delistedAdapter{}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true), Fills: &consumer.Reducer{Store: tradeStore},
	}
	ctx := context.Background()

	// 轻量同步先跑：本地持仓和交易所不一致，会去查下架标的的成交。
	_, err := service.SyncAccountLight(ctx, "account-1")
	require.NoError(t, err, "轻量同步")
	require.Contains(t, adapter.fillQueries, delistedSymbol, "本地持仓与交易所不一致会带进下架标的，要实际查过")
	adapter.fillQueries = nil
	_, err = service.SyncAccountReconcile(ctx, "account-1")
	require.NoError(t, err, "对账")
	require.Contains(t, adapter.fillQueries, delistedSymbol, "未结束的单会带进下架标的，要实际查过")

	result, err := service.SyncAccount(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready)
	account, err := tradeStore.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready, "账户要就绪，否则清仓、减仓、目标执行都被挡住")
	require.Equal(t, "41", account.FillCursors["SWAP:"+delistedSymbol], "游标原样保留")

	for _, id := range []string{"gone-unknown", "gone-open", "gone-canceling"} {
		record, err := tradeStore.GetOrder(ctx, "space-1", id)
		require.NoError(t, err)
		require.Equal(t, "EXPIRED", record.State, id)
		require.Equal(t, InstrumentGoneReason, record.RejectReason, id)
		require.Equal(t, "0", record.RemainingReservedQuantity, "资金预留要释放："+id)
		require.NotZero(t, record.FinishedAt, id)
	}
	for _, id := range []string{"gone-pending", "gone-submitting"} {
		record, err := tradeStore.GetOrder(ctx, "space-1", id)
		require.NoError(t, err)
		require.NotEqual(t, "EXPIRED", record.State, "待提交、刚提交中的单不动："+id)
	}
	positions, err := tradeStore.ListPositions(ctx, "space-1", "account-1", exchange.SymbolKey{})
	require.NoError(t, err)
	require.Empty(t, positions, "持仓以交易所同步结果为准")

	notes := telemetry.Recent(telemetry.AttentionOrdersInstrumentGone, time.Now(), time.Hour)
	for _, id := range []string{"gone-unknown", "gone-open", "gone-canceling"} {
		require.True(t, containsDetail(notes, id), "要记关注项："+id)
	}

	// 再同步一次照常成功，已结束的单不再重复处理。
	result, err = service.SyncAccountFull(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready)
}

// 提交中的单停了超过 5 分钟（进程在提交途中退出）、标的又已下架：同样按过期结束。
func TestSyncClosesStaleSubmittingOrderOfDelistedInstrument(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	seedDelisted(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &delistedAdapter{}},
		SessionState: readySessionState(true), Fills: &consumer.Reducer{Store: tradeStore},
		Now: func() time.Time { return time.Now().Add(10 * time.Minute) },
	}
	_, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	record, err := tradeStore.GetOrder(context.Background(), "space-1", "gone-submitting")
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", record.State)
	record, err = tradeStore.GetOrder(context.Background(), "space-1", "gone-pending")
	require.NoError(t, err)
	require.Equal(t, "PENDING", record.State)
}

func containsDetail(details []string, needle string) bool {
	for _, detail := range details {
		if strings.Contains(detail, needle) {
			return true
		}
	}
	return false
}
