package accountsync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type noOpenOrdersAdapter struct{ *syncAdapter }

func (noOpenOrdersAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) { return nil, nil }

type idleSyncAdapter struct{ noOpenOrdersAdapter }

func (idleSyncAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}

func TestRotationCoversFullSetInTenRounds(t *testing.T) {
	full := make([]string, 0, 25)
	for index := 0; index < 25; index++ {
		full = append(full, fmt.Sprintf("S%02d", index))
	}
	service := &Service{}
	seen := make(map[string]struct{})
	for round := 0; round < rotationBatches; round++ {
		batch := service.nextRotationBatch("account-1", full)
		require.Len(t, batch, 3)
		for _, symbol := range batch {
			seen[symbol] = struct{}{}
		}
	}
	require.Len(t, seen, len(full))
}

func TestChangedAssetsFindsDifferencesAndDisappearedBalances(t *testing.T) {
	changed := changedAssets(
		[]store.AssetBalance{{Asset: "BTC", Total: "1"}, {Asset: "ETH", Total: "2"}, {Asset: "SOL", Total: "3"}},
		[]exchange.AssetBalance{
			{Asset: "BTC", Total: shared.MustDecimal("1")},
			{Asset: "ETH", Total: shared.MustDecimal("2.5")},
		},
	)
	require.Equal(t, map[string]struct{}{"ETH": {}, "SOL": {}}, changed)
}

func TestIncrementalSyncSkipsIdleSymbolsOutsideRotation(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	full := make([]string, 0, 500)
	for index := 0; index < 500; index++ {
		full = append(full, fmt.Sprintf("S%03d", index))
	}
	service := &Service{Store: tradeStore}
	symbols, err := service.incrementalSymbols(
		context.Background(), account,
		[]exchange.Order{{ExchangeSymbol: "ACTIVE"}}, nil,
		[]exchange.Position{{ExchangeSymbol: "MOVED", SignedQuantity: shared.MustDecimal("1")}},
		nil, exchange.AccountSnapshot{}, full, true, 0,
	)
	require.NoError(t, err)
	// 轮转一次最多 rotationLimit 个标的。
	require.Len(t, symbols, rotationLimit+2)
	require.Contains(t, symbols, "ACTIVE")
	require.Contains(t, symbols, "MOVED")
	// 不推进轮转（撤单确认、未知订单收尾）时只拉有信号的标的。
	symbols, err = service.incrementalSymbols(
		context.Background(), account,
		[]exchange.Order{{ExchangeSymbol: "ACTIVE"}}, nil,
		[]exchange.Position{{ExchangeSymbol: "MOVED", SignedQuantity: shared.MustDecimal("1")}},
		nil, exchange.AccountSnapshot{}, full, false, 0,
	)
	require.NoError(t, err)
	require.Equal(t, []string{"ACTIVE", "MOVED"}, symbols)
}

// 接入前手工交易过的标的，第一次拉成交不能入账，也不能算外部成交。
func TestFillsBeforeAccountCreationOnlyAdvanceCursor(t *testing.T) {
	tradeStore := openSyncStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			CreatedAt: time.UnixMilli(5_000), SpaceID: "space-1", TradingAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SWAP", ExecutionMode: "LIVE", Environment: "TESTNET",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}))
	adapter := noOpenOrdersAdapter{&syncAdapter{fill: exchange.Fill{
		ExchangeTradeID: "old-trade", ExchangeOrderID: "old-order", ClientOrderID: "old-client",
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("100"), SettlementAsset: "USDT",
		TradedAt: time.UnixMilli(1_000),
	}}}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter}, SessionState: readySessionState(true),
		Fills: &consumer.Reducer{Store: tradeStore}, Now: func() time.Time { return time.UnixMilli(6_000) },
	}
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Zero(t, result.FillsIngested)
	require.False(t, result.ExternalFactsImported)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.NotEmpty(t, account.FillCursors["BTC-USDT"])
	require.True(t, account.Ready)
}

// 接入前部分成交的挂单：创建前的成交被过滤，累计成交从交易所的值减去本批成交起步。
func TestImportedOrderBeforeCreationSeedsFilledQuantity(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SWAP", ExchangeSymbol: "BTC-USDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			Linear: true, ContractValue: "1", ContractValueAsset: "BTC", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	account.CreatedAt = time.UnixMilli(5_000)
	service := Service{Store: tradeStore}
	record, err := service.importExternalOrder(context.Background(), account, exchange.Order{
		ExchangeOrderID: "order-1", ClientOrderID: "client-1", ExchangeSymbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
		FilledQuantity: shared.MustDecimal("0.6"), AveragePrice: shared.MustDecimal("100"),
		Status: exchange.OrderStatusPartiallyFilled, CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(6_000),
	}, shared.MustDecimal("0.2"))
	require.NoError(t, err)
	require.Equal(t, "0.4", record.FilledQuantity)
	require.Equal(t, "100", record.AveragePrice)
}

// 旧于已存时间的私有更新不合并余额，但账户确实变过，要关闭准入等完整同步。
func TestStalePrivateUpdateKeepsBalanceButClosesAdmission(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: idleSyncAdapter{noOpenOrdersAdapter{&syncAdapter{}}}}, SessionState: readySessionState(true),
		Fills: &consumer.Reducer{Store: tradeStore}, Now: func() time.Time { return time.UnixMilli(4_000) },
	}
	_, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	before, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)

	require.NoError(t, service.ApplyAccountSnapshot(context.Background(), "account-1", exchange.AccountSnapshot{
		AvailableFunds: shared.MustDecimal("1"), ExchangeUpdatedAt: time.UnixMilli(before.Snapshot.ExchangeUpdatedAt - 1),
		Present: exchange.AccountSnapshotPresence{AvailableFunds: true},
	}))

	after, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, after.Ready)
	require.Equal(t, before.Snapshot.AvailableFunds, after.Snapshot.AvailableFunds)
	require.NotZero(t, after.BalancePendingUntil)
}

// 同一交易所的两把不同密钥各自同步，一个账户的成交不会变成另一个账户的外部成交。
func TestTwoKeysOnSameExchangeSyncIndependently(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			CreatedAt: time.UnixMilli(1), SpaceID: "space-1", TradingAccountID: "account-2", Name: "second",
			Exchange: "BINANCE", MarketType: "SWAP", ExecutionMode: "LIVE", Environment: "TESTNET",
			CredentialSecretID: "secret-2", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}))
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: idleSyncAdapter{noOpenOrdersAdapter{&syncAdapter{}}}},
		SessionState: readySessionState(true), Fills: &consumer.Reducer{Store: tradeStore},
		Now: func() time.Time { return time.UnixMilli(3_000) },
	}
	for _, accountID := range []string{"account-1", "account-2"} {
		result, err := service.SyncAccount(context.Background(), accountID)
		require.NoError(t, err)
		require.False(t, result.ExternalFactsImported)
	}
}

func TestManualAccountKeepsPauseReasonOnExternalFact(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'`).Error)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
		})
	}))
	service := Service{Store: tradeStore}

	require.NoError(t, service.pauseForExternalFact(context.Background(), "account-1"))

	logical, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.False(t, store.IsAutoPauseReason(logical.PauseReason))
}

// 会话启动和手动同步：500 个有游标但没有变化的标的不能一次全拉。
func TestBoundedFullSymbolsCapsIdleSymbols(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	account.FillCursors = make(map[string]string, 500)
	for index := 0; index < 500; index++ {
		account.FillCursors[fmt.Sprintf("S%03dUSDT", index)] = "1"
	}
	service := &Service{Store: tradeStore}
	open := []exchange.Order{{ExchangeSymbol: "OPENUSDT"}}
	symbols, err := service.BoundedFullSymbols(context.Background(), account, open, nil, nil, nil, exchange.AccountSnapshot{})
	require.NoError(t, err)
	require.LessOrEqual(t, len(symbols), fullSymbolCap)
	require.Contains(t, symbols, "OPENUSDT")
}
