package accountsync

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type fixedSpotPrice struct{ at time.Time }

func (p fixedSpotPrice) GetReferencePrice(_ context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	if key.Market != exchange.MarketTypeSpot {
		return exchange.ReferencePrice{Price: shared.MustDecimal("51000"), UpdatedAt: p.at}, nil
	}
	return exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: p.at}, nil
}

// 同步写库的权益按统一公式算（结算资产各钱包净额 + 永续未实现盈亏 + 受管现货市值），
// 不用适配器各自口径的 Equity：币安现货没有、币安合约是 totalMarginBalance、OKX 只有结算币 cashBal。
func TestSyncWritesUnifiedEquity(t *testing.T) {
	const now = int64(1_700_000_000_000)
	cases := []struct {
		name     string
		exchange string
		mode     exchange.AccountMode
		markets  []string
		symbol   string
		snapshot exchange.AccountSnapshot
		want     string
	}{
		{
			name: "币安普通账户只开现货", exchange: "BINANCE", mode: exchange.AccountModeBinanceStandard,
			markets: []string{"SPOT"}, symbol: "BTCUSDT",
			snapshot: exchange.AccountSnapshot{
				Present: exchange.AccountSnapshotPresence{Balances: true},
				Balances: []exchange.AssetBalance{
					{Wallet: exchange.WalletSpot, Asset: "USDT", Available: shared.MustDecimal("500"), Total: shared.MustDecimal("500")},
					{Wallet: exchange.WalletSpot, Asset: "BTC", Available: shared.MustDecimal("0.01"), Total: shared.MustDecimal("0.01")},
				},
			},
			want: "1000", // 500 + 0.01×50000
		},
		{
			name: "币安普通账户现货加合约", exchange: "BINANCE", mode: exchange.AccountModeBinanceStandard,
			markets: []string{"SPOT", "SWAP"}, symbol: "BTCUSDT",
			snapshot: exchange.AccountSnapshot{
				Present: exchange.AccountSnapshotPresence{Balances: true, Equity: true, UnrealizedPnL: true},
				Balances: []exchange.AssetBalance{
					{Wallet: exchange.WalletSpot, Asset: "USDT", Available: shared.MustDecimal("500"), Total: shared.MustDecimal("500")},
					{Wallet: exchange.WalletSpot, Asset: "BTC", Available: shared.MustDecimal("0.01"), Total: shared.MustDecimal("0.01")},
					{Wallet: exchange.WalletFutures, Asset: "USDT", Available: shared.MustDecimal("900"), Total: shared.MustDecimal("1000")},
				},
				Equity: shared.MustDecimal("1020"), UnrealizedPnL: shared.MustDecimal("20"),
			},
			want: "2020", // 500 + 1000 + 20 + 0.01×50000
		},
		{
			name: "OKX 现货加永续", exchange: "OKX", mode: exchange.AccountModeOKXUnified,
			markets: []string{"SPOT", "SWAP"}, symbol: "BTC-USDT",
			snapshot: exchange.AccountSnapshot{
				Present: exchange.AccountSnapshotPresence{Balances: true, Equity: true, UnrealizedPnL: true},
				Balances: []exchange.AssetBalance{
					{Wallet: exchange.WalletUnified, Asset: "USDT", Available: shared.MustDecimal("1000"), Total: shared.MustDecimal("1000")},
					{Wallet: exchange.WalletUnified, Asset: "BTC", Available: shared.MustDecimal("0.01"), Total: shared.MustDecimal("0.01")},
				},
				Equity: shared.MustDecimal("1000"), UnrealizedPnL: shared.MustDecimal("20"),
			},
			want: "1520", // 1000 + 20 + 0.01×50000
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tradeStore := openSyncStore(t)
			require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
				if err := tx.CreateTradingAccount(store.TradingAccountRecord{
					CreatedAt: time.UnixMilli(1), SpaceID: "space-1", TradingAccountID: "account-1", Name: tc.name,
					Exchange: tc.exchange, Markets: tc.markets, AccountMode: string(tc.mode),
					ExecutionMode: "LIVE", Environment: "PRODUCTION", CredentialSecretID: "secret-1",
					SettlementAsset: "USDT", MarginMode: "CROSS", LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "5"},
					Status: "ENABLED", Ready: true,
				}); err != nil {
					return err
				}
				if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
					SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: "LIVE",
					SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "test",
				}); err != nil {
					return err
				}
				if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
					SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true,
				}); err != nil {
					return err
				}
				if err := tx.UpsertInstrument(store.InstrumentRecord{
					Exchange: tc.exchange, MarketType: "SPOT", ExchangeSymbol: tc.symbol, InstrumentID: "BTC-USDT-SPOT",
					BaseAsset: "BTC", QuoteAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
					PriceTick: "0.1", Status: "TRADING",
				}); err != nil {
					return err
				}
				return tx.CreateOrder(store.OrderRecord{
					SpaceID: "space-1", TradingAccountID: "account-1", OrderID: "bought", ClientOrderID: "bought",
					Exchange: tc.exchange, MarketType: "SPOT", ExchangeSymbol: tc.symbol, InstrumentID: "BTC-USDT-SPOT",
					OrderType: "MARKET", Side: "BUY", Quantity: "0.01", FilledQuantity: "0.01", ReferencePrice: "50000",
					State: "FILLED", OwnerType: "OPERATOR", OwnerID: "action-1", LogicalAccountID: "logical-1", Version: 1,
				})
			}))
			service := &Service{
				Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
				SessionState: readySessionState(true), Fills: &consumer.Reducer{Store: tradeStore},
				Equity: &equity.Service{Store: tradeStore, Now: func() time.Time { return time.UnixMilli(now) }},
				Now:    func() time.Time { return time.UnixMilli(now) },
			}
			snapshot := tc.snapshot
			snapshot.ExchangeUpdatedAt = time.UnixMilli(now - 1)
			_, err := service.ApplySnapshot(ctx, "account-1", Snapshot{
				Account: snapshot, AccountReadAt: time.UnixMilli(now - 1), Ready: true,
				Prices: fixedSpotPrice{at: time.UnixMilli(now)},
			})
			require.NoError(t, err)
			account, err := tradeStore.GetTradingAccountByID(ctx, "account-1")
			require.NoError(t, err)
			require.Equal(t, tc.want, account.Snapshot.Equity)
		})
	}
}
