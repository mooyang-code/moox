package wiring

import (
	"context"
	"path/filepath"
	"testing"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestCredentialFromSecretChecksMetadataAndParsesExtra(t *testing.T) {
	_, err := CredentialFromSecret(accountapp.ExchangeSecret{
		SecretID: "s", Category: "exchange", Exchange: exchange.ExchangeOKX,
		Status: "active", KeyID: "key", SecretValue: "secret",
	}, exchange.ExchangeBinance, "s")
	require.ErrorContains(t, err, "元数据不匹配")

	credential, err := CredentialFromSecret(accountapp.ExchangeSecret{
		SecretID: "s", Category: "exchange", Exchange: exchange.ExchangeOKX,
		Status: "active", KeyID: "key", SecretValue: "secret",
		ExtraConfig: `{"passphrase":"pass","readonly":"false"}`,
	}, exchange.ExchangeOKX, "s")
	require.NoError(t, err)
	require.Equal(t, "key", credential.APIKey)
	require.Equal(t, "secret", credential.APISecret)
	require.Equal(t, "pass", credential.Passphrase)
	require.Equal(t, "false", credential.Extra["readonly"])
}

func TestInstrumentSourceCarriesPriceRatiosAndRejectsMarketMismatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "acc-1", Name: "test",
			Exchange: "binance", Markets: []string{"spot"},
			ExecutionMode: "LIVE", Environment: "TESTNET",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", MarketType: "spot", Environment: "TESTNET",
			ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ContractValue: "1", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
			PriceTick: "0.01", MinNotional: "5",
			MaxBuyPriceRatio: "1.05", MinSellPriceRatio: "0.95",
			Status: "TRADING", ExchangeUpdatedAt: 1_000,
		})
	}))

	source := InstrumentSource{Store: db}
	got, err := source.GetInstrumentForAccount(ctx, "acc-1", exchange.ExchangeBinance, exchange.MarketTypeSpot, "BTC-USDT-SPOT")
	require.NoError(t, err)
	require.Equal(t, "1.05", got.MaxBuyPriceRatio.String())
	require.Equal(t, "0.95", got.MinSellPriceRatio.String())

	_, err = source.GetInstrumentForAccount(ctx, "acc-1", exchange.ExchangeBinance, exchange.MarketTypeSwap, "BTC-USDT-SPOT")
	require.ErrorContains(t, err, "属于市场 spot，不是 swap")
}

func TestRegisterCryptoExchangesValidatesCredentialsAndComposesMultiMarket(t *testing.T) {
	registry := execution.NewRegistry()
	RegisterCryptoExchanges(registry)

	_, err := registry.Bind(exchange.AccountConfig{
		TradingAccountID: "acc-okx", Exchange: exchange.ExchangeOKX,
		Markets:       []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		ExecutionMode: exchange.ExecutionModeLive, AccountMode: exchange.AccountModeOKXUnified,
		Environment: exchange.AccountEnvironmentTestnet, SettlementAsset: "USDT", MarginMode: exchange.MarginModeCross,
	}, exchange.Credential{APIKey: "k", APISecret: "s"})
	require.ErrorContains(t, err, "passphrase")

	adapter, err := registry.Bind(exchange.AccountConfig{
		TradingAccountID: "acc-okx", Exchange: exchange.ExchangeOKX,
		Markets:       []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		ExecutionMode: exchange.ExecutionModeLive, AccountMode: exchange.AccountModeOKXUnified,
		Environment: exchange.AccountEnvironmentTestnet, SettlementAsset: "USDT", MarginMode: exchange.MarginModeCross,
	}, exchange.Credential{APIKey: "k", APISecret: "s", Passphrase: "p"})
	require.NoError(t, err)
	_, isMulti := adapter.(*execution.MultiMarket)
	require.True(t, isMulti, "OKX 双市场应由注册表合成 MultiMarket")
}
