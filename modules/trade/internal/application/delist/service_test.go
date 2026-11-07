package delist

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type accountSpec struct {
	id, exchange, mode, environment string
	markets                         []string
	logical                         string // 空表示不在任何组合里
	balances                        []store.AssetBalance
	status                          string
}

func newDelistStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

func seedAccounts(t *testing.T, tradeStore *store.Store, fundSettings string, specs ...accountSpec) {
	t.Helper()
	logicals := map[string]bool{}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, spec := range specs {
			status := spec.status
			if status == "" {
				status = "ENABLED"
			}
			record := store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: spec.id, Name: spec.id, Exchange: spec.exchange, Markets: spec.markets,
				ExecutionMode: spec.mode, Environment: spec.environment, SettlementAsset: "USDT", Status: status, Ready: true,
				Snapshot: store.TradingAccountSnapshot{Balances: spec.balances}, LastSyncAt: 1, SnapshotSourceTime: 1,
			}
			if spec.mode == "LIVE" {
				record.CredentialSecretID = "secret-" + spec.id
				record.AccountMode = map[string]string{"BINANCE": "BINANCE_STANDARD", "OKX": "OKX_UNIFIED"}[spec.exchange]
			}
			// 余额写成空钱包：按账户自己的现货钱包补上。
			balances := make([]store.AssetBalance, len(spec.balances))
			copy(balances, spec.balances)
			for i := range balances {
				balances[i].Wallet = string(record.WalletFor("SPOT"))
			}
			record.Snapshot.Balances = balances
			if err := tx.CreateTradingAccount(record); err != nil {
				return err
			}
			if spec.logical == "" {
				continue
			}
			if !logicals[spec.logical] {
				logicals[spec.logical] = true
				if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
					SpaceID: "space-1", LogicalAccountID: spec.logical, Name: spec.logical, ExecutionMode: spec.mode,
					SettlementAsset: "USDT", OwnerInstanceID: "i-" + spec.logical, OwnerSessionID: "s", FundSettingsJSON: fundSettings,
					AutomationState: "PAUSED", PauseReason: "configure",
				}); err != nil {
					return err
				}
			}
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: spec.logical, TradingAccountID: spec.id, Enabled: true, Priority: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}

func seedInstruments(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, market := range []string{"SPOT", "SWAP"} {
			record := store.InstrumentRecord{
				Exchange: "BINANCE", Environment: "PRODUCTION", MarketType: market, ExchangeSymbol: "FOOUSDT", InstrumentID: "FOO-USDT-" + market,
				BaseAsset: "FOO", QuoteAsset: "USDT", SettlementAsset: "USDT", ExchangeQuantityStep: "0.1", MinExchangeQuantity: "1", PriceTick: "0.01", Status: "TRADING",
			}
			if market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", "FOO"
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
		}
		return nil
	}))
}

// markManaged 让现货资产成为受管资产：MooX 的策略订单在这个标的上有成交。
func markManaged(t *testing.T, tradeStore *store.Store, accountID, orderID string) {
	t.Helper()
	account, err := tradeStore.GetTradingAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	logicalID := ""
	if logical, _, err := tradeStore.FindLogicalAccountMembership(context.Background(), "space-1", accountID); err == nil {
		logicalID = logical.LogicalAccountID
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: accountID, OrderID: orderID, ClientOrderID: orderID,
			Exchange: account.Exchange, MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", InstrumentID: "FOO-USDT-SPOT",
			OrderType: "MARKET", Side: "BUY", Quantity: "5", FilledQuantity: "5", ReferencePrice: "1",
			State: "FILLED", OwnerType: "OPERATOR", OwnerID: "t", LogicalAccountID: logicalID, Version: 1,
		})
	}))
}

func spotBalance(total string) []store.AssetBalance {
	return []store.AssetBalance{{Wallet: "", Asset: "FOO", Available: total, Total: total}}
}

// 未知标的：照常写入名单、不算失败，返回无此标的的标记；持有者查询返回空数组加同样的标记。
func TestBlockUnknownInstrumentIsStoredAndReported(t *testing.T) {
	tradeStore := newDelistStore(t)
	service := &Service{Store: tradeStore}
	calls := 0
	service.OnChanged = func() { calls++ }
	key := Key{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "BARUSDT"}

	result, err := service.Block(context.Background(), key, "公告下架", "evt-1")
	require.NoError(t, err)
	require.True(t, result.InstrumentUnknown)
	require.True(t, result.Created)
	require.Equal(t, 1, calls)

	again, err := service.Block(context.Background(), key, "公告下架", "evt-1")
	require.NoError(t, err)
	require.False(t, again.Created, "重复写入不报错也不改动")
	require.Equal(t, 1, calls, "没有变化不唤醒")

	list, err := service.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "", list[0].InstrumentID, "查不到标的时内部 ID 留空")

	holders, err := service.Holders(context.Background(), key, false)
	require.NoError(t, err)
	require.True(t, holders.InstrumentUnknown)
	require.NotNil(t, holders.Holders, "无此标的时也要返回空数组，不是省略")
	require.Empty(t, holders.Holders)

	removed, err := service.Unblock(context.Background(), key)
	require.NoError(t, err)
	require.True(t, removed)
	require.Equal(t, 2, calls)
}

func TestBlockRecordsInstrumentIDAndBaseAssetWhenKnown(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	service := &Service{Store: tradeStore}

	result, err := service.Block(context.Background(), Key{Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "FOOUSDT"}, "公告下架", "")
	require.NoError(t, err)
	require.False(t, result.InstrumentUnknown)
	list, err := service.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, "FOO-USDT-SWAP", list[0].InstrumentID)
	require.Equal(t, "FOO", list[0].BaseAsset)
}

func TestBlockValidatesTheKey(t *testing.T) {
	service := &Service{Store: newDelistStore(t)}
	for _, key := range []Key{
		{Exchange: "", MarketType: "SPOT", ExchangeSymbol: "A"},
		{Exchange: "BINANCE", MarketType: "STOCK", ExchangeSymbol: "A"},
		{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: " "},
	} {
		_, err := service.Block(context.Background(), key, "r", "")
		require.ErrorIs(t, err, ErrInvalidRequest, "%+v", key)
	}
	_, err := service.Block(context.Background(), Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "A"}, " ", "")
	require.ErrorIs(t, err, ErrInvalidRequest, "reason 必填")
}

// 只算策略持有：受管现货、非零永续；只看该交易所；不在组合的、测试网、没有受管的、已停用的都不算。
func TestHoldersOnlyCountStrategyHoldings(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "live", exchange: "BINANCE", mode: "LIVE", environment: "PRODUCTION", markets: []string{"SPOT"}, logical: "logical-live", balances: spotBalance("12.5")},
		accountSpec{id: "paper", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-paper", balances: spotBalance("3")},
		accountSpec{id: "testnet", exchange: "BINANCE", mode: "LIVE", environment: "TESTNET", markets: []string{"SPOT"}, logical: "logical-testnet", balances: spotBalance("9")},
		accountSpec{id: "okx", exchange: "OKX", mode: "LIVE", environment: "PRODUCTION", markets: []string{"SPOT"}, logical: "logical-live", balances: spotBalance("9")},
		accountSpec{id: "loose", exchange: "BINANCE", mode: "LIVE", environment: "PRODUCTION", markets: []string{"SPOT"}, balances: spotBalance("9")},
		accountSpec{id: "unmanaged", exchange: "BINANCE", mode: "LIVE", environment: "PRODUCTION", markets: []string{"SPOT"}, logical: "logical-live", balances: spotBalance("9")},
		accountSpec{id: "disabled", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-paper", balances: spotBalance("9"), status: "DISABLED"},
		accountSpec{id: "dust", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-paper", balances: spotBalance("0.5")},
		accountSpec{id: "empty", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-paper"},
	)
	for _, id := range []string{"live", "paper", "testnet", "okx", "disabled", "dust", "empty"} { // loose 不在组合里，没有组合订单
		markManaged(t, tradeStore, id, "bought-"+id)
	}
	service := &Service{Store: tradeStore}
	key := Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"}

	result, err := service.Holders(context.Background(), key, false)
	require.NoError(t, err)
	require.False(t, result.InstrumentUnknown)
	got := map[string]Holder{}
	for _, holder := range result.Holders {
		got[holder.TradingAccountID] = holder
	}
	require.Len(t, got, 4, "live、paper、dust 和已停用的 disabled（停用的账户照常列出并带状态）")
	require.Equal(t, "DISABLED", got["disabled"].Status)
	require.Equal(t, "ENABLED", got["live"].Status)
	require.Equal(t, Holder{SpaceID: "space-1", LogicalAccountID: "logical-live", TradingAccountID: "live", Quantity: "12.5", Status: "ENABLED", Note: "取不到报价，零头只按最小数量判断"}, got["live"])
	require.True(t, got["paper"].Paper)
	require.False(t, got["live"].Paper)
	require.True(t, got["dust"].Dust, "低于最小数量 1 的是零头")
	require.False(t, got["live"].Dust)

	paperOnly, err := service.Holders(context.Background(), key, true)
	require.NoError(t, err)
	require.Len(t, paperOnly.Holders, 3, "paper、dust、disabled")
	for _, holder := range paperOnly.Holders {
		require.True(t, holder.Paper, "paper_only 时每一项都必须是模拟盘")
	}

	// 没有任何持有者：空数组、没有无此标的标记。
	none, err := service.Holders(context.Background(), Key{Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "FOOUSDT"}, false)
	require.NoError(t, err)
	require.False(t, none.InstrumentUnknown)
	require.NotNil(t, none.Holders)
	require.Empty(t, none.Holders)
}

// 永续：非零持仓才算，套利底仓的对冲腿不算。
func TestHoldersSwapSkipsFlatAndArbitrageHedgeLegs(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "long", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SWAP"}, logical: "logical-1"},
		accountSpec{id: "short", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SWAP"}, logical: "logical-1"},
		accountSpec{id: "flat", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SWAP"}, logical: "logical-1"},
	)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for id, quantity := range map[string]string{"long": "7", "short": "-0.4", "flat": "0"} {
			if err := tx.UpsertPosition(store.PositionRecord{
				SpaceID: "space-1", TradingAccountID: id, MarketType: "SWAP", ExchangeSymbol: "FOOUSDT", PositionSide: "NET",
				SignedQuantity: quantity, EntryPrice: "1", MarkPrice: "1", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	service := &Service{Store: tradeStore}
	key := Key{Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "FOOUSDT"}

	result, err := service.Holders(context.Background(), key, false)
	require.NoError(t, err)
	got := map[string]Holder{}
	for _, holder := range result.Holders {
		got[holder.TradingAccountID] = holder
	}
	require.Len(t, got, 2)
	require.Equal(t, "7", got["long"].Quantity)
	require.Equal(t, "-0.4", got["short"].Quantity, "空头带符号")
	require.True(t, got["short"].Dust, "0.4 低于最小数量 1")

	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"arbitrage_base_assets":["FOO"]}'`).Error)
	hedged, err := service.Holders(context.Background(), key, false)
	require.NoError(t, err)
	require.Empty(t, hedged.Holders, "套利底仓的永续对冲腿不是策略持有")
}

type fixedPrice string

func (p fixedPrice) LatestPrice(context.Context, string, exchange.SymbolKey) (targetapp.Quote, error) {
	return targetapp.Quote{Price: shared.MustDecimal(string(p)), UpdatedAt: time.Now()}, nil
}

// 零头和清仓同一个口径：有报价时还看最小金额（价格暴跌的下架币常常高于最小数量、低于最小金额）；取不到报价时按最小数量并标注。
func TestHoldersDustUsesMinimumNotionalWhenThereIsAQuote(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_trade_instruments SET c_min_notional = '5' WHERE c_market_type = 'SPOT'`).Error)
	tradeStore.InvalidateCaches()
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "paper", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-paper", balances: spotBalance("3")})
	markManaged(t, tradeStore, "paper", "bought-paper")
	key := Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"}

	// 3 个、价格 1：价值 3 低于最小金额 5，是零头；数量 3 高于最小数量 1。
	result, err := (&Service{Store: tradeStore, Prices: fixedPrice("1")}).Holders(context.Background(), key, false)
	require.NoError(t, err)
	require.Len(t, result.Holders, 1)
	require.True(t, result.Holders[0].Dust)
	require.Empty(t, result.Holders[0].Note)

	result, err = (&Service{Store: tradeStore, Prices: fixedPrice("2")}).Holders(context.Background(), key, false)
	require.NoError(t, err)
	require.False(t, result.Holders[0].Dust, "价值 6 不低于最小金额")

	result, err = (&Service{Store: tradeStore}).Holders(context.Background(), key, false)
	require.NoError(t, err)
	require.False(t, result.Holders[0].Dust, "没有报价只按最小数量判断")
	require.Contains(t, result.Holders[0].Note, "取不到报价")
}

// 一个组合出错（资金设置读不出来）不能让整个跨空间查询失败：跳过它并在响应里标出，其余照常。
func TestHoldersSkipsAnAccountThatErrorsInsteadOfFailingEverything(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "good", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-good", balances: spotBalance("7")},
		accountSpec{id: "broken", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-broken", balances: spotBalance("7")},
	)
	markManaged(t, tradeStore, "good", "bought-good")
	markManaged(t, tradeStore, "broken", "bought-broken")
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"arbitrage_base_assets":"BTC"}' WHERE c_logical_account_id = 'logical-broken'`).Error)

	result, err := (&Service{Store: tradeStore}).Holders(context.Background(), Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT"}, false)

	require.NoError(t, err)
	require.Len(t, result.Holders, 1)
	require.Equal(t, "good", result.Holders[0].TradingAccountID)
	require.Len(t, result.SkippedAccounts, 1)
	require.Equal(t, "broken", result.SkippedAccounts[0].TradingAccountID)
	require.NotEmpty(t, result.SkippedAccounts[0].Reason)
}

// 标的的计价币不是账户的结算币（屏蔽 FOOBTC，账户按 USDT 结算）：清仓什么也不会卖，不能算持有，写进说明。
func TestHoldersIgnoreInstrumentsQuotedInANonSettlementAsset(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "PRODUCTION", MarketType: "SPOT", ExchangeSymbol: "FOOBTC", InstrumentID: "FOO-BTC-SPOT",
			BaseAsset: "FOO", QuoteAsset: "BTC", SettlementAsset: "BTC", ExchangeQuantityStep: "0.1", MinExchangeQuantity: "1", PriceTick: "0.00000001", Status: "TRADING",
		})
	}))
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "paper", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT"}, logical: "logical-paper", balances: spotBalance("9")})
	markManaged(t, tradeStore, "paper", "bought-paper")

	result, err := (&Service{Store: tradeStore}).Holders(context.Background(), Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "FOOBTC"}, false)

	require.NoError(t, err)
	require.Empty(t, result.Holders)
	require.Len(t, result.Notes, 1)
	require.Contains(t, result.Notes[0], "计价币不是结算币")
}

// 交易所代码统一转大写：调用方传小写也能对上标的，不会被判成“Trade 无此标的”而漏掉真实的下架。
func TestKeysAreCaseInsensitive(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	service := &Service{Store: tradeStore}
	lower := Key{Exchange: "binance", MarketType: "swap", ExchangeSymbol: "foousdt"}

	result, err := service.Block(context.Background(), lower, "公告下架", "")
	require.NoError(t, err)
	require.False(t, result.InstrumentUnknown)
	list, err := service.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, "FOOUSDT", list[0].ExchangeSymbol)
	holders, err := service.Holders(context.Background(), lower, false)
	require.NoError(t, err)
	require.False(t, holders.InstrumentUnknown)
	removed, err := service.Unblock(context.Background(), lower)
	require.NoError(t, err)
	require.True(t, removed)
}

func TestResolveInstrumentIDRequiresAMemberOfTheExchange(t *testing.T) {
	tradeStore := newDelistStore(t)
	seedInstruments(t, tradeStore)
	seedAccounts(t, tradeStore, "",
		accountSpec{id: "binance", exchange: "BINANCE", mode: "PAPER", environment: "PAPER", markets: []string{"SPOT", "SWAP"}, logical: "logical-1"},
	)
	service := &Service{Store: tradeStore}

	id, err := service.ResolveInstrumentID(context.Background(), "space-1", "logical-1", Key{Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "FOOUSDT"})
	require.NoError(t, err)
	require.Equal(t, "FOO-USDT-SWAP", id)

	_, err = service.ResolveInstrumentID(context.Background(), "space-1", "logical-1", Key{Exchange: "OKX", MarketType: "SWAP", ExchangeSymbol: "FOO-USDT-SWAP"})
	require.ErrorIs(t, err, ErrNoMemberOfExchange)

	_, err = service.ResolveInstrumentID(context.Background(), "space-1", "logical-1", Key{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "NOPEUSDT"})
	require.ErrorIs(t, err, ErrInvalidRequest, "成员的标的表里没有它时不能静默当作平完")
}
