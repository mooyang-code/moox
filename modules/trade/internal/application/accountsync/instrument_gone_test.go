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
// 待提交、刚提交中的单不动；成交游标保留（删掉的话标的一旦又查得到，币安会从头重拉成交）；持仓以交易所为准（交易所已经没有这笔持仓）。
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
	require.Equal(t, "41", account.FillCursors["SWAP:"+delistedSymbol], "下架标的的成交游标原样保留")

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

// 下架标的的单带着成交缺口：下单回执、订单快照抬高了观察累计成交量，明细还没入账标的就下架了，查成交只回 -1121，
// 缺口永远补不上。同步要在入账快照之前收掉这个标的上的缺口（挂单中的和已结束的单都算），成交游标保留，记关注项，
// 账户恢复就绪；否则账户一直停在“成交待余额同步确认”。
func TestSyncSettlesFillGapsOfDelistedInstrument(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
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
		for _, id := range []string{"gap-open", "gap-filled", "gap-expired"} {
			limit := "1"
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", OrderID: id, TradingAccountID: "account-1", ClientOrderID: id + "-client",
				ExchangeOrderID: id + "-exchange", MarketType: "SWAP", ExchangeSymbol: delistedSymbol, OrderType: "LIMIT",
				TimeInForce: "GTC", Side: "SELL", PositionSide: "NET", Quantity: "2", LimitPrice: &limit,
				ReferencePrice: "1", ReferencePriceAt: 1_000, ReduceOnly: true, OwnerType: "OPERATOR", OwnerID: "delist-1",
				LogicalAccountID: "logical-1", State: "OPEN", FilledQuantity: "0", ReservedAsset: "USDT",
				ReservedQuantity: "5", RemainingReservedQuantity: "5", SubmittedAt: time.Now().UnixMilli(), Version: 1,
			}); err != nil {
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

	adapter := &delistedAdapter{}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true), Fills: &consumer.Reducer{Store: tradeStore},
	}
	// 生产路径造缺口：交易所订单更新报了成交（明细还没到），一笔部分成交仍挂着，一笔交易所已报全部成交
	// （本地要等明细入账才落成 FILLED，所以仍是 OPEN），一笔交易所报过期、本地落成终态。
	exchangeOrder := func(id string, status exchange.OrderStatus, filled shared.Decimal) exchange.Order {
		limit := shared.MustDecimal("1")
		return exchange.Order{
			Market: exchange.MarketTypeSwap, ExchangeSymbol: delistedSymbol, ClientOrderID: id + "-client",
			ExchangeOrderID: id + "-exchange", OrderType: exchange.OrderTypeLimit, Side: exchange.SideSell,
			PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("2"), LimitPrice: &limit,
			FilledQuantity: filled, Status: status,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_500),
		}
	}
	require.NoError(t, service.ApplyOrder(ctx, "account-1", exchangeOrder("gap-open", exchange.OrderStatusPartiallyFilled, shared.MustDecimal("1"))))
	require.NoError(t, service.ApplyOrder(ctx, "account-1", exchangeOrder("gap-filled", exchange.OrderStatusFilled, shared.MustDecimal("2"))))
	require.NoError(t, service.ApplyOrder(ctx, "account-1", exchangeOrder("gap-expired", exchange.OrderStatusExpired, shared.MustDecimal("1"))))
	expired, err := tradeStore.GetOrder(ctx, "space-1", "gap-expired")
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", expired.State, "前提：已结束的单带着缺口，不经过 closeInstrumentGone")
	gaps, err := tradeStore.CountOrdersWithFillGap(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Equal(t, 3, gaps)
	account, err = tradeStore.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Equal(t, awaitingBalanceReason, account.LastError)
	require.True(t, account.HasBalancePending())

	result, err := service.SyncAccount(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready, "%v", result.Warnings)
	require.Contains(t, adapter.fillQueries, delistedSymbol, "缺口会带进下架标的，要实际查过")

	account, err = tradeStore.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready, "缺口收掉后账户要就绪：%s", account.LastError)
	require.False(t, account.HasBalancePending())
	require.Equal(t, "41", account.FillCursors["SWAP:"+delistedSymbol], "下架标的的成交游标原样保留")
	require.Contains(t, account.FillCursors, "SWAP:BTC-USDT", "别的标的的游标不动")
	gaps, err = tradeStore.CountOrdersWithFillGap(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Zero(t, gaps)
	for _, id := range []string{"gap-open", "gap-filled", "gap-expired"} {
		record, err := tradeStore.GetOrder(ctx, "space-1", id)
		require.NoError(t, err)
		require.Equal(t, "EXPIRED", record.State, id)
		require.Equal(t, "0", record.FilledQuantity, "没有明细就不凭空入账："+id)
		var observed string
		require.NoError(t, tradeStore.DBForTest().Raw(
			`SELECT c_observed_filled_quantity FROM t_trade_orders WHERE c_order_id = ?`, id).Scan(&observed).Error)
		require.Equal(t, "0", observed, "观察成交量压回已入账值："+id)
	}

	notes := telemetry.Recent(telemetry.AttentionOrdersInstrumentGone, time.Now(), time.Hour)
	for _, id := range []string{"gap-open", "gap-filled", "gap-expired"} {
		found := false
		for _, detail := range notes {
			found = found || strings.Contains(detail, id) && strings.Contains(detail, "成交明细缺失")
		}
		require.True(t, found, "要记关注项（带单号和“成交明细缺失”）：%s %v", id, notes)
	}

	// 再同步：缺口、未结束的单都没有了，对账不再查下架标的；完整同步按保留的游标照样查一次，回“标的不存在”只跳过、记提示。
	// 两次账户都照样就绪，缺口已经收掉，也不再重复记关注项。
	for _, detail := range notes {
		if strings.Contains(detail, "gap-") {
			telemetry.Forget(telemetry.AttentionOrdersInstrumentGone, detail)
		}
	}
	adapter.fillQueries = nil
	result, err = service.SyncAccountReconcile(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.NotContains(t, adapter.fillQueries, delistedSymbol, "缺口、未结束的单都没有了，对账不再查下架标的")
	result, err = service.SyncAccountFull(ctx, "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.Contains(t, adapter.fillQueries, delistedSymbol, "游标保留，完整同步照样查")
	require.True(t, containsDetail(result.Warnings, "跳过成交查询"), "%v", result.Warnings)
	account, err = tradeStore.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.Equal(t, "41", account.FillCursors["SWAP:"+delistedSymbol], "游标一直保留")
	for _, id := range []string{"gap-open", "gap-filled", "gap-expired"} {
		require.False(t, containsDetail(telemetry.Recent(telemetry.AttentionOrdersInstrumentGone, time.Now(), time.Hour), id),
			"缺口已收掉，不再重复记关注项：%s", id)
	}
}
