package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type fakeExchangeSecretSource struct {
	secrets []ExchangeSecret
}

func (f fakeExchangeSecretSource) ListExchangeSecrets(ctx context.Context, provider string) ([]ExchangeSecret, error) {
	if provider != "binance" {
		return nil, nil
	}
	return f.secrets, nil
}

type importSecretStore struct {
	Store
	accounts map[string]*Account
	keys     map[string]*APIKey
	channels map[string]*TradeChannel
	balances map[string][]*Balance
}

func newImportSecretStore() *importSecretStore {
	return &importSecretStore{
		accounts: map[string]*Account{},
		keys:     map[string]*APIKey{},
		channels: map[string]*TradeChannel{},
		balances: map[string][]*Balance{},
	}
}

func (s *importSecretStore) CreateAccount(ctx context.Context, spaceID string, a *Account) error {
	cp := *a
	cp.SpaceID = spaceID
	s.accounts[a.AccountID] = &cp
	return nil
}

func (s *importSecretStore) UpdateAccount(ctx context.Context, spaceID string, a *Account) error {
	cp := *a
	cp.SpaceID = spaceID
	s.accounts[a.AccountID] = &cp
	return nil
}

func (s *importSecretStore) GetAccount(ctx context.Context, spaceID, accountID string) (*Account, error) {
	if a, ok := s.accounts[accountID]; ok {
		return a, nil
	}
	return nil, ErrNotFound
}

func (s *importSecretStore) CreateAPIKey(ctx context.Context, spaceID string, k *APIKey) error {
	cp := *k
	cp.SpaceID = spaceID
	s.keys[k.APIKeyID] = &cp
	return nil
}

func (s *importSecretStore) GetAPIKey(ctx context.Context, spaceID, apiKeyID string) (*APIKey, error) {
	if k, ok := s.keys[apiKeyID]; ok {
		return k, nil
	}
	return nil, ErrNotFound
}

func (s *importSecretStore) CreateChannel(ctx context.Context, spaceID string, c *TradeChannel) error {
	cp := *c
	cp.SpaceID = spaceID
	s.channels[c.ChannelID] = &cp
	return nil
}

func (s *importSecretStore) UpdateChannel(ctx context.Context, spaceID string, c *TradeChannel) error {
	cp := *c
	cp.SpaceID = spaceID
	s.channels[c.ChannelID] = &cp
	return nil
}

func (s *importSecretStore) GetChannel(ctx context.Context, spaceID, channelID string) (*TradeChannel, error) {
	if ch, ok := s.channels[channelID]; ok {
		return ch, nil
	}
	return nil, ErrNotFound
}

func (s *importSecretStore) UpsertBalances(ctx context.Context, spaceID string, balances []*Balance) error {
	for _, b := range balances {
		cp := *b
		cp.SpaceID = spaceID
		s.balances[b.AccountID] = append(s.balances[b.AccountID], &cp)
	}
	return nil
}

func (s *importSecretStore) GetBalances(ctx context.Context, spaceID, accountID string, currencies []string) ([]*Balance, error) {
	return s.balances[accountID], nil
}

func TestSyncExchangeAccountsImportsBinanceSecrets(t *testing.T) {
	store := newImportSecretStore()
	svc := New("trade", WithStore(store), WithExchangeSecretSource(fakeExchangeSecretSource{
		secrets: []ExchangeSecret{
			{
				SecretID:    "secret-one",
				Name:        "币安-921667055",
				Provider:    "binance",
				KeyID:       "public-key",
				SecretValue: "plain-secret",
			},
			{
				SecretID:    "secret-two",
				Name:        "binance-spot",
				Provider:    "binance",
				KeyID:       "spot-key",
				SecretValue: "spot-secret",
				ExtraConfig: `{"market_type":"spot","permissions":["read","trade"]}`,
			},
		},
	}))

	accounts, err := svc.Account.SyncExchangeAccounts(context.Background(), "crypto", SyncExchangeAccountsOptions{
		UserID:     "user_1",
		Provider:   "binance",
		MarketType: "swap",
	})
	if err != nil {
		t.Fatalf("SyncExchangeAccounts returned error: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("imported accounts = %d, want 2", len(accounts))
	}

	swap := accounts[0]
	if swap.AccountName != "币安-921667055" || swap.AccountType != AccountSwap || swap.BaseCurrency != "USDT" {
		t.Fatalf("unexpected default account: %+v", swap)
	}
	if got := store.keys[deterministicID("ak", "secret-one")]; got == nil || got.APIKey != "public-key" || got.APISecret != "plain-secret" {
		t.Fatalf("unexpected imported api key: %+v", got)
	}
	if got := store.channels[deterministicID("ch", "secret-one")]; got == nil || got.MarketType != "swap" || got.APIKeyID == "" {
		t.Fatalf("unexpected imported channel: %+v", got)
	}

	spot := accounts[1]
	if spot.AccountType != AccountSpot {
		t.Fatalf("extra_config market_type not applied: %+v", spot)
	}
}

type marketProbeAdapter struct {
	exchange.ExchangeAdapter
	available map[string]map[exchange.MarketType]bool
}

func (a marketProbeAdapter) GetBalances(ctx context.Context, cred exchange.Credential, market exchange.MarketType, currencies []string) ([]exchange.Balance, error) {
	if a.available[cred.APIKey][market] {
		return []exchange.Balance{{Currency: "USDT", Available: "1", Total: "1"}}, nil
	}
	return nil, fmt.Errorf("%s unavailable for %s", market, cred.APIKey)
}

func (a marketProbeAdapter) ListPositions(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) ([]exchange.Position, error) {
	if market != exchange.MarketSwap {
		return nil, nil
	}
	return []exchange.Position{{
		Symbol: "STOUSDT", PosSide: "long", Quantity: "519",
		AvgPrice: "0.10", Leverage: "5", Margin: "10",
	}}, nil
}

func TestSyncExchangeAccountsAutoDetectsAvailableMarkets(t *testing.T) {
	store := newImportSecretStore()
	adapter := marketProbeAdapter{
		available: map[string]map[exchange.MarketType]bool{
			"both-key": {
				exchange.MarketSpot: true,
				exchange.MarketSwap: true,
			},
			"spot-key": {
				exchange.MarketSpot: true,
			},
		},
	}
	svc := New(
		"trade",
		WithStore(store),
		WithExchangeSecretSource(fakeExchangeSecretSource{
			secrets: []ExchangeSecret{
				{SecretID: "secret-both", Name: "币安-多市场", Provider: "binance", KeyID: "both-key", SecretValue: "both-secret", ExtraConfig: "{}"},
				{SecretID: "secret-spot", Name: "币安-现货", Provider: "binance", KeyID: "spot-key", SecretValue: "spot-secret", ExtraConfig: "{}"},
			},
		}),
		WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
			if name != "binance" {
				t.Fatalf("exchange name = %q, want binance", name)
			}
			return adapter, nil
		}),
	)

	accounts, err := svc.Account.SyncExchangeAccounts(context.Background(), "crypto", SyncExchangeAccountsOptions{
		UserID:   "user_1",
		Provider: "binance",
	})
	if err != nil {
		t.Fatalf("SyncExchangeAccounts returned error: %v", err)
	}
	if len(accounts) != 3 {
		t.Fatalf("imported accounts = %d, want 3", len(accounts))
	}

	wantAccounts := map[string]AccountType{
		deterministicID("acc", "secret-both|spot"): AccountSpot,
		deterministicID("acc", "secret-both|swap"): AccountSwap,
		deterministicID("acc", "secret-spot|spot"): AccountSpot,
	}
	for accountID, wantType := range wantAccounts {
		got := store.accounts[accountID]
		if got == nil {
			t.Fatalf("account %s not imported; accounts=%v", accountID, store.accounts)
		}
		if got.AccountType != wantType {
			t.Fatalf("account %s type = %s, want %s", accountID, got.AccountType, wantType)
		}
		ch := store.channels[deterministicID("ch", accountID)]
		if ch == nil || ch.AccountID != accountID || ch.APIKeyID == "" {
			t.Fatalf("channel for %s not imported correctly: %+v", accountID, ch)
		}
	}
}

func TestSyncExchangeAccountsWithSnapshotsImportsBalancesAndSwapPositions(t *testing.T) {
	store := newImportSecretStore()
	adapter := marketProbeAdapter{
		available: map[string]map[exchange.MarketType]bool{
			"both-key": {
				exchange.MarketSpot: true,
				exchange.MarketSwap: true,
			},
		},
	}
	svc := New(
		"trade",
		WithStore(store),
		WithExchangeSecretSource(fakeExchangeSecretSource{
			secrets: []ExchangeSecret{
				{SecretID: "secret-both", Name: "币安-多市场", Provider: "binance", KeyID: "both-key", SecretValue: "both-secret", ExtraConfig: "{}"},
			},
		}),
		WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
			return adapter, nil
		}),
	)

	accounts, err := svc.SyncExchangeAccountsWithSnapshots(context.Background(), "crypto", SyncExchangeAccountsOptions{
		UserID:   "user_1",
		Provider: "binance",
	})
	if err != nil {
		t.Fatalf("SyncExchangeAccountsWithSnapshots returned error: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("imported accounts = %d, want 2", len(accounts))
	}
	spotID := deterministicID("acc", "secret-both|spot")
	swapID := deterministicID("acc", "secret-both|swap")
	if len(store.balances[spotID]) == 0 || len(store.balances[swapID]) == 0 {
		t.Fatalf("balances not synced: spot=%v swap=%v", store.balances[spotID], store.balances[swapID])
	}
}
