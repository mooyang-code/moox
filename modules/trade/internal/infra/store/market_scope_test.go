package store

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func exchangeKey(market string) exchange.SymbolKey {
	return exchange.NewSymbolKey(market, "BTCUSDT")
}

func seedTwoMarketAccount(t *testing.T, s *Store) {
	t.Helper()
	account := testAccount()
	account.Markets = []string{"SPOT", "SWAP"}
	account.MarginMode = "CROSS"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		for _, market := range []string{"SPOT", "SWAP"} {
			record := InstrumentRecord{
				Exchange: "BINANCE", MarketType: market, ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-" + market,
				BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", PriceTick: "0.01",
				ExchangeQuantityStep: "0.001", Status: "TRADING",
			}
			if market == "SWAP" {
				record.Linear, record.ContractValue, record.ContractValueAsset = true, "1", "BTC"
				record.MinExchangeQuantity = "0.001"
			}
			if err := tx.UpsertInstrument(record); err != nil {
				return err
			}
		}
		return nil
	}))
}

func orderIn(market, id string) OrderRecord {
	return OrderRecord{
		SpaceID: "space-1", OrderID: "order-" + id, TradingAccountID: "account-1", ClientOrderID: "client-" + id,
		ExchangeOrderID: "100", MarketType: market, ExchangeSymbol: "BTCUSDT", OrderType: "MARKET",
		Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "ext-" + id, State: "OPEN",
	}
}

// 订单的市场必须是账户已开通的市场；只开通一个市场的账户可以省略，开通多个时必须写明。
func TestCreateOrderRequiresMarketOpenedByTheAccount(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedTwoMarketAccount(t, s)

	create := func(record OrderRecord) error {
		return s.Transaction(ctx, func(tx *Tx) error { return tx.CreateOrder(record) })
	}
	require.ErrorIs(t, create(orderIn("", "missing")), ErrInvalidRecord, "开通两个市场时订单必须写明市场")
	require.ErrorIs(t, create(orderIn("STOCK", "stock")), ErrInvalidRecord, "没有开通的市场")
	require.NoError(t, create(orderIn("SPOT", "spot")))
	// 同一个交易所订单号在另一个市场是另一张订单；同市场重复仍被唯一索引拒绝。
	require.NoError(t, create(orderIn("SWAP", "swap")))
	duplicate := orderIn("SPOT", "dup")
	require.ErrorIs(t, create(duplicate), ErrConflict)

	spot, err := s.GetOrderByExchangeID(ctx, "space-1", "account-1", exchangeKey("SPOT"), "100")
	require.NoError(t, err)
	require.Equal(t, "order-spot", spot.OrderID)
	require.Equal(t, "BTC-USDT-SPOT", spot.InstrumentID)
	swap, err := s.GetOrderByExchangeID(ctx, "space-1", "account-1", exchangeKey("SWAP"), "100")
	require.NoError(t, err)
	require.Equal(t, "order-swap", swap.OrderID)
	require.Equal(t, "BTC-USDT-SWAP", swap.InstrumentID)

	single := openTestStore(t)
	require.NoError(t, single.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01", ExchangeQuantityStep: "0.001", Status: "TRADING",
		})
	}))
	derived := orderIn("", "derived")
	require.NoError(t, single.Transaction(ctx, func(tx *Tx) error { return tx.CreateOrder(derived) }), "单市场账户可以省略市场")
	got, err := single.GetOrder(ctx, "space-1", "order-derived")
	require.NoError(t, err)
	require.Equal(t, "SPOT", got.MarketType)
}

func TestUpsertPositionRequiresMarketOpenedByTheAccount(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedTwoMarketAccount(t, s)
	position := PositionRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
		SignedQuantity: "1", Leverage: "5", ExchangeUpdatedAt: 1,
	}
	require.ErrorIs(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertPosition(position) }), ErrInvalidRecord, "两个市场时持仓必须写明市场")
	position.MarketType = "STOCK"
	require.ErrorIs(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertPosition(position) }), ErrInvalidRecord)
	position.MarketType = "SWAP"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.UpsertPosition(position) }))
}

func TestSnapshotRoundTripsWalletsAndRejectsUnknownWallet(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	account := testAccount()
	account.Snapshot = TradingAccountSnapshot{
		Balances: []AssetBalance{
			{Wallet: "SPOT", Asset: "USDT", Available: "10", Total: "10"},
			{Wallet: "FUTURES", Asset: "USDT", Available: "90", Locked: "10", Total: "100"},
			{Wallet: "SPOT", Asset: "BTC", Available: "1", Total: "1"},
		},
		Equity: "110", ExchangeEquity: "111", ExchangeUpdatedAt: 5_000,
		WalletUpdatedAt: map[string]int64{"SPOT": 4_000},
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateTradingAccount(account) }))

	got, err := s.GetTradingAccount(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Equal(t, "111", got.Snapshot.ExchangeEquity)
	require.Equal(t, int64(4_000), got.Snapshot.WalletUpdatedAt["SPOT"])
	require.Equal(t, int64(5_000), got.Snapshot.WalletUpdatedAt["FUTURES"], "没有单独记录更新时间的钱包取快照时间")
	require.Len(t, got.Snapshot.WalletBalances("SPOT"), 2)
	require.Len(t, got.Snapshot.WalletBalances("FUTURES"), 1)
	require.Equal(t, "10", got.Snapshot.WalletBalances("FUTURES")[0].Locked)

	bad := testAccount()
	bad.TradingAccountID, bad.Name = "account-2", "bad"
	bad.Snapshot.Balances = []AssetBalance{{Wallet: "MAIN", Asset: "USDT", Total: "1"}}
	require.ErrorIs(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateTradingAccount(bad) }), ErrInvalidRecord)
	bad.Snapshot.Balances = []AssetBalance{{Wallet: "SPOT", Asset: "USDT", Total: "1"}, {Wallet: "SPOT", Asset: "USDT", Total: "2"}}
	require.ErrorIs(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateTradingAccount(bad) }), ErrInvalidRecord, "同一钱包里同一资产只能有一项")
}

func TestCreateTradingAccountRejectsNonCanonicalMarkets(t *testing.T) {
	s := openTestStore(t)
	for _, markets := range [][]string{{"SWAP", "SPOT"}, {"SPOT", "SPOT"}, {}, {"STOCK", "SPOT"}, {"FUTURES"}} {
		account := testAccount()
		account.Markets = markets
		err := s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(account) })
		require.ErrorIs(t, err, ErrInvalidRecord, "%v", markets)
	}
}

func TestOneCredentialCanHaveOnlyOneEnabledLiveAccount(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	live := func(id, space, status string) TradingAccountRecord {
		account := testAccount()
		account.SpaceID, account.TradingAccountID, account.Name = space, id, id
		account.ExecutionMode, account.Environment, account.CredentialSecretID = "LIVE", "PRODUCTION", "secret-x"
		account.Status = status
		return account
	}
	create := func(account TradingAccountRecord) error {
		return s.Transaction(ctx, func(tx *Tx) error { return tx.CreateTradingAccount(account) })
	}
	require.NoError(t, create(live("a1", "space-1", "ENABLED")))
	require.ErrorIs(t, create(live("a2", "space-1", "ENABLED")), ErrConflict)
	require.ErrorIs(t, create(live("a3", "space-2", "ENABLED")), ErrConflict, "跨空间也一样")
	require.NoError(t, create(live("a4", "space-2", "DISABLED")), "停用的账户不占用密钥")
}
