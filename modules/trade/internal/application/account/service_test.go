package account

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestCreateValidatesAggregateAndCredential(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*tradingaccount.Account)
		secret     ExchangeSecret
		want       error
		wantWrites int
	}{
		{
			name: "valid SPOT paper account without credential",
			mutate: func(value *tradingaccount.Account) {
				value.CredentialSecretID = ""
			},
			wantWrites: 1,
		},
		{
			name:   "missing ID",
			mutate: func(value *tradingaccount.Account) { value.ID = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing SpaceID",
			mutate: func(value *tradingaccount.Account) { value.SpaceID = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing name",
			mutate: func(value *tradingaccount.Account) { value.Name = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing Exchange",
			mutate: func(value *tradingaccount.Account) { value.Exchange = exchange.ExchangeUnspecified },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing market",
			mutate: func(value *tradingaccount.Account) { value.MarketType = exchange.MarketTypeUnspecified },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "missing execution mode",
			mutate: func(value *tradingaccount.Account) {
				value.ExecutionMode = exchange.ExecutionModeUnspecified
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "missing credential secret ID",
			mutate: func(value *tradingaccount.Account) {
				value.ExecutionMode = exchange.ExecutionModeLive
				value.Environment = exchange.AccountEnvironmentTestnet
				value.CredentialSecretID = ""
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "missing settlement asset",
			mutate: func(value *tradingaccount.Account) {
				value.SettlementAsset = ""
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "wrong credential category",
			mutate: func(value *tradingaccount.Account) {
				value.ExecutionMode = exchange.ExecutionModeLive
				value.Environment = exchange.AccountEnvironmentTestnet
			},
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "cloud",
				Exchange: exchange.ExchangeBinance, Status: "active",
			},
			want: ErrInvalidCredential,
		},
		{
			name: "wrong credential Exchange",
			mutate: func(value *tradingaccount.Account) {
				value.ExecutionMode = exchange.ExecutionModeLive
				value.Environment = exchange.AccountEnvironmentTestnet
			},
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeOKX, Status: "active",
			},
			want: ErrInvalidCredential,
		},
		{
			name: "inactive credential",
			mutate: func(value *tradingaccount.Account) {
				value.ExecutionMode = exchange.ExecutionModeLive
				value.Environment = exchange.AccountEnvironmentTestnet
			},
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeBinance, Status: "disabled",
			},
			want: ErrInvalidCredential,
		},
		{
			name:   "SPOT rejects margin",
			mutate: func(value *tradingaccount.Account) { value.MarginMode = exchange.MarginModeCross },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "SPOT rejects leverage",
			mutate: func(value *tradingaccount.Account) {
				value.LeverageSettings = map[string]shared.Decimal{"BTC-USDT": shared.MustDecimal("2")}
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "SWAP requires CROSS",
			mutate: func(value *tradingaccount.Account) {
				value.MarketType = exchange.MarketTypeSwap
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := validAccount()
			if tt.mutate != nil {
				tt.mutate(&value)
			}
			store := newMemoryStore()
			secrets := &fakeSecrets{secret: tt.secret}
			service := Service{Store: store, Secrets: secrets}

			_, err := service.Create(context.Background(), value)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Create() error = %v, want %v", err, tt.want)
			}
			if store.createCalls != tt.wantWrites {
				t.Fatalf("Create() writes = %d, want %d", store.createCalls, tt.wantWrites)
			}
		})
	}
}

func TestCreateDoesNotAliasSameNameAcrossExecutionModes(t *testing.T) {
	store := newMemoryStore()
	paper := validAccount()
	store.accounts[paper.ID] = paper
	store.nameIndex[paper.SpaceID+"\x00"+paper.Name] = paper.ID

	live := paper
	live.ID = "account-live"
	live.ExecutionMode = exchange.ExecutionModeLive
	live.Environment = exchange.AccountEnvironmentTestnet
	secrets := &fakeSecrets{
		secret: validSecret(),
	}
	service := Service{Store: store, Secrets: secrets}

	_, err := service.Create(context.Background(), live)
	if !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("Create() error = %v, want ErrAccountConflict", err)
	}
	if got := store.accounts[paper.ID]; got.ExecutionMode != exchange.ExecutionModePaper {
		t.Fatalf("existing execution mode = %q, want PAPER", got.ExecutionMode)
	}
	if store.createCalls != 1 {
		t.Fatalf("Create() calls = %d, want one rejected insert", store.createCalls)
	}
}

func TestCreateProductionAccountRequiresExplicitLiveTrading(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	value.ExecutionMode = exchange.ExecutionModeLive
	value.Environment = exchange.AccountEnvironmentProduction
	value.SyncSymbols = []string{"BTCUSDT"}
	secrets := &fakeSecrets{
		secret: validSecret(),
	}
	service := Service{Store: store, Secrets: secrets}

	_, err := service.Create(context.Background(), value)
	if !errors.Is(err, ErrLiveTradingDisabled) {
		t.Fatalf("Create() error = %v, want ErrLiveTradingDisabled", err)
	}
	if secrets.getCalls != 0 {
		t.Fatalf("GetExchangeSecret() calls = %d, want 0", secrets.getCalls)
	}
	if store.createCalls != 0 {
		t.Fatalf("Create() writes = %d, want 0", store.createCalls)
	}
}

func TestCreatePaperAccountDoesNotRequireSecretSource(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	value.CredentialSecretID = ""
	service := Service{Store: store}

	got, err := service.Create(context.Background(), value)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got.CredentialSecretID != "" || store.createCalls != 1 {
		t.Fatalf("Create() = %+v, writes = %d", got, store.createCalls)
	}
}

func TestUpdateChangesOnlyExplicitMutableFields(t *testing.T) {
	store := newMemoryStore()
	before := validSwapAccount()
	store.accounts[before.ID] = before
	service := Service{Store: store, Secrets: &fakeSecrets{secret: validSecret()}}
	name := "renamed"

	got, err := service.Update(context.Background(), UpdateCommand{
		TradingAccountID: before.ID,
		Name:             &name,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.Name != name {
		t.Fatalf("Name = %q, want %q", got.Name, name)
	}
	if got.Exchange != before.Exchange ||
		got.MarketType != before.MarketType ||
		got.ExecutionMode != before.ExecutionMode ||
		got.CredentialSecretID != before.CredentialSecretID ||
		got.SettlementAsset != before.SettlementAsset ||
		got.MarginMode != before.MarginMode {
		t.Fatalf("Update() changed immutable or unspecified fields: before=%+v after=%+v", before, got)
	}
}

func TestUpdateEnablingProductionAccountRequiresLiveTrading(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	value.ExecutionMode = exchange.ExecutionModeLive
	value.Environment = exchange.AccountEnvironmentProduction
	value.Status = exchange.AccountStatusDisabled
	store.accounts[value.ID] = value
	status := exchange.AccountStatusEnabled
	service := Service{
		Store: store,
		Secrets: &fakeSecrets{
			secret: validSecret(),
		},
	}

	_, err := service.Update(context.Background(), UpdateCommand{
		TradingAccountID: value.ID,
		Status:           &status,
	})
	if !errors.Is(err, ErrLiveTradingDisabled) {
		t.Fatalf("Update() error = %v, want ErrLiveTradingDisabled", err)
	}
	if store.updateCalls != 0 {
		t.Fatalf("Update() writes = %d, want 0", store.updateCalls)
	}
}

func TestExecutionEligibilityUsesCurrentSessionState(t *testing.T) {
	tests := []struct {
		name    string
		status  exchange.AccountStatus
		ready   bool
		wantErr bool
	}{
		{name: "enabled ready", status: exchange.AccountStatusEnabled, ready: true},
		{name: "disabled", status: exchange.AccountStatusDisabled, ready: true, wantErr: true},
		{name: "not ready", status: exchange.AccountStatusEnabled, ready: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			value := validAccount()
			value.Status = tt.status
			store.accounts[value.ID] = value
			service := Service{
				Store:        store,
				SessionState: fakeSessionState{ready: tt.ready},
			}

			_, err := service.ExecutionEligibility(context.Background(), value.ID)
			if tt.wantErr && !errors.Is(err, tradingaccount.ErrAccountNotExecutable) {
				t.Fatalf("ExecutionEligibility() error = %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ExecutionEligibility() error = %v", err)
			}
		})
	}
}

func TestExecutionEligibilityRejectsMissingSessionState(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	value.Ready = true
	store.accounts[value.ID] = value
	service := Service{Store: store}

	_, err := service.ExecutionEligibility(context.Background(), value.ID)
	if !errors.Is(err, ErrServiceNotConfigured) {
		t.Fatalf("ExecutionEligibility() error = %v, want ErrServiceNotConfigured", err)
	}
}

func TestExecutionEligibilityRejectsProductionWhenLiveTradingDisabled(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	value.ExecutionMode = exchange.ExecutionModeLive
	value.Environment = exchange.AccountEnvironmentProduction
	value.SyncSymbols = []string{"BTCUSDT"}
	store.accounts[value.ID] = value
	service := Service{Store: store, SessionState: fakeSessionState{ready: true}}

	_, err := service.ExecutionEligibility(context.Background(), value.ID)
	if !errors.Is(err, ErrLiveTradingDisabled) {
		t.Fatalf("ExecutionEligibility() error = %v, want ErrLiveTradingDisabled", err)
	}

	service.LiveTradingEnabled = true
	if _, err := service.ExecutionEligibility(context.Background(), value.ID); err != nil {
		t.Fatalf("ExecutionEligibility() with live enabled error = %v", err)
	}
}

func TestSetLeverageAndPauseUseCommandSpecificWrites(t *testing.T) {
	store := newMemoryStore()
	value := validSwapAccount()
	store.accounts[value.ID] = value
	service := Service{Store: store}

	if err := service.SetLeverage(
		context.Background(),
		value.ID,
		"ETH-USDT",
		shared.MustDecimal("3"),
	); err != nil {
		t.Fatalf("SetLeverage() error = %v", err)
	}
	if store.leverageCalls != 1 || store.updateCalls != 0 {
		t.Fatalf("leverage writes=%d aggregate writes=%d", store.leverageCalls, store.updateCalls)
	}
}

func validAccount() tradingaccount.Account {
	return tradingaccount.Account{
		ID:                 "account-1",
		SpaceID:            "space-1",
		Name:               "main",
		Exchange:           exchange.ExchangeBinance,
		MarketType:         exchange.MarketTypeSpot,
		ExecutionMode:      exchange.ExecutionModePaper,
		Environment:        exchange.AccountEnvironmentPaper,
		CredentialSecretID: "secret-1",
		SettlementAsset:    "USDT",
		Status:             exchange.AccountStatusEnabled,
	}
}

func validSwapAccount() tradingaccount.Account {
	value := validAccount()
	value.MarketType = exchange.MarketTypeSwap
	value.MarginMode = exchange.MarginModeCross
	value.LeverageSettings = map[string]shared.Decimal{
		"BTC-USDT": shared.MustDecimal("5"),
	}
	return value
}

func validSecret() ExchangeSecret {
	return ExchangeSecret{
		SecretID: "secret-1", Category: "exchange",
		Exchange: exchange.ExchangeBinance, Status: "active",
	}
}

type memoryStore struct {
	accounts      map[string]tradingaccount.Account
	nameIndex     map[string]string
	createCalls   int
	updateCalls   int
	leverageCalls int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		accounts:  make(map[string]tradingaccount.Account),
		nameIndex: make(map[string]string),
	}
}

func (s *memoryStore) Create(_ context.Context, value tradingaccount.Account) error {
	s.createCalls++
	key := value.SpaceID + "\x00" + value.Name
	if _, found := s.nameIndex[key]; found {
		return ErrAccountConflict
	}
	s.accounts[value.ID] = value
	s.nameIndex[key] = value.ID
	return nil
}

func (s *memoryStore) Get(_ context.Context, id string) (tradingaccount.Account, error) {
	value, found := s.accounts[id]
	if !found {
		return tradingaccount.Account{}, ErrAccountNotFound
	}
	return value, nil
}

func (s *memoryStore) Update(_ context.Context, command UpdateCommand) error {
	s.updateCalls++
	value, found := s.accounts[command.TradingAccountID]
	if !found {
		return ErrAccountNotFound
	}
	applyUpdate(&value, command)
	s.accounts[value.ID] = value
	return nil
}

func (s *memoryStore) SetLeverage(
	_ context.Context,
	id string,
	symbol string,
	leverage shared.Decimal,
) error {
	s.leverageCalls++
	value, found := s.accounts[id]
	if !found {
		return ErrAccountNotFound
	}
	if value.LeverageSettings == nil {
		value.LeverageSettings = make(map[string]shared.Decimal)
	}
	value.LeverageSettings[symbol] = leverage
	s.accounts[id] = value
	return nil
}

type fakeSecrets struct {
	secret   ExchangeSecret
	err      error
	getCalls int
}

func (f *fakeSecrets) GetExchangeSecret(
	_ context.Context,
	_ string,
) (ExchangeSecret, error) {
	f.getCalls++
	return f.secret, f.err
}

type fakeSessionState struct{ ready bool }

func (f fakeSessionState) ReadyFor(tradingaccount.Account) bool { return f.ready }
func (f fakeSessionState) Invalidate(string)                    {}
