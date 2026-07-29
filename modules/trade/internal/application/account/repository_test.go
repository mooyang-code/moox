package account

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestRepositoryPersistsAccountAndLeverage(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	repository := Repository{Store: tradeStore}
	value := exchangeaccount.Account{
		ID: "account-1", SpaceID: "space-1", Name: "main",
		Exchange: exchange.ExchangeOKX, MarketType: exchange.MarketTypeSwap,
		ExecutionMode:      exchange.ExecutionModeLive,
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		MarginMode: exchange.MarginModeCross, Status: exchange.AccountStatusEnabled,
		LeverageSettings: map[string]shared.Decimal{},
	}
	if err := repository.Create(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetLeverage(
		context.Background(),
		value.ID,
		"BTC-USDT-SWAP",
		shared.MustDecimal("5"),
	); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.LeverageSettings["BTC-USDT-SWAP"].String(); got != "5" {
		t.Fatalf("leverage = %q, want 5", got)
	}
	errorsCh := make(chan error, 2)
	for symbol, leverage := range map[string]string{
		"ETH-USDT-SWAP": "10",
		"SOL-USDT-SWAP": "3",
	} {
		go func(symbol string, leverage string) {
			errorsCh <- repository.SetLeverage(
				context.Background(),
				value.ID,
				symbol,
				shared.MustDecimal(leverage),
			)
		}(symbol, leverage)
	}
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	stored, err = repository.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.LeverageSettings) != 3 {
		t.Fatalf("leverage settings = %v, want three symbols", stored.LeverageSettings)
	}
	duplicate := value
	duplicate.SpaceID = "space-2"
	if err := repository.Create(context.Background(), duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate global account ID error = %v, want conflict", err)
	}
}
