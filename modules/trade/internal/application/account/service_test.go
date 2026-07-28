package account

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestCreateValidatesAggregateAndCredential(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*exchangeaccount.Account)
		secret     ExchangeSecret
		want       error
		wantWrites int
	}{
		{
			name: "valid SPOT paper account",
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeBinance, Status: "active",
			},
			wantWrites: 1,
		},
		{
			name:   "missing ID",
			mutate: func(value *exchangeaccount.Account) { value.ID = "" },
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name:   "missing SpaceID",
			mutate: func(value *exchangeaccount.Account) { value.SpaceID = "" },
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name:   "missing name",
			mutate: func(value *exchangeaccount.Account) { value.Name = "" },
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name:   "missing Exchange",
			mutate: func(value *exchangeaccount.Account) { value.Exchange = exchange.ExchangeUnspecified },
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name:   "missing market",
			mutate: func(value *exchangeaccount.Account) { value.MarketType = exchange.MarketTypeUnspecified },
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name: "missing execution mode",
			mutate: func(value *exchangeaccount.Account) {
				value.ExecutionMode = exchange.ExecutionModeUnspecified
			},
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name: "missing credential secret ID",
			mutate: func(value *exchangeaccount.Account) {
				value.CredentialSecretID = ""
			},
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name: "missing settlement asset",
			mutate: func(value *exchangeaccount.Account) {
				value.SettlementAsset = ""
			},
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name: "wrong credential category",
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "cloud",
				Exchange: exchange.ExchangeBinance, Status: "active",
			},
			want: ErrInvalidCredential,
		},
		{
			name: "wrong credential Exchange",
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeOKX, Status: "active",
			},
			want: ErrInvalidCredential,
		},
		{
			name: "inactive credential",
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeBinance, Status: "disabled",
			},
			want: ErrInvalidCredential,
		},
		{
			name:   "SPOT rejects margin",
			mutate: func(value *exchangeaccount.Account) { value.MarginMode = exchange.MarginModeCross },
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name: "SPOT rejects leverage",
			mutate: func(value *exchangeaccount.Account) {
				value.LeverageSettings = map[string]shared.Decimal{"BTC-USDT": shared.MustDecimal("2")}
			},
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
		{
			name: "SWAP requires CROSS",
			mutate: func(value *exchangeaccount.Account) {
				value.MarketType = exchange.MarketTypeSwap
			},
			secret: validSecret(),
			want:   exchangeaccount.ErrInvalidAccount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := validAccount()
			if tt.mutate != nil {
				tt.mutate(&value)
			}
			store := newMemoryStore()
			secrets := &fakeSecrets{secrets: []ExchangeSecret{tt.secret}}
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
	secrets := &fakeSecrets{
		secrets: []ExchangeSecret{validSecret()},
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

func TestCreateLiveAccountFailsClosedBeforeReadingCredential(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	value.ExecutionMode = exchange.ExecutionModeLive
	secrets := &fakeSecrets{
		secrets:    []ExchangeSecret{validSecret()},
		liveKeyErr: ErrLiveCredentialAccess,
	}
	service := Service{Store: store, Secrets: secrets}

	_, err := service.Create(context.Background(), value)
	if !errors.Is(err, ErrLiveCredentialAccess) {
		t.Fatalf("Create() error = %v, want ErrLiveCredentialAccess", err)
	}
	if secrets.listCalls != 0 {
		t.Fatalf("ListExchangeSecrets() calls = %d, want 0", secrets.listCalls)
	}
	if store.createCalls != 0 {
		t.Fatalf("Create() writes = %d, want 0", store.createCalls)
	}
}

func TestUpdateChangesOnlyExplicitMutableFields(t *testing.T) {
	store := newMemoryStore()
	before := validSwapAccount()
	store.accounts[before.ID] = before
	service := Service{Store: store, Secrets: &fakeSecrets{secrets: []ExchangeSecret{validSecret()}}}
	name := "renamed"

	got, err := service.Update(context.Background(), UpdateCommand{
		ExchangeAccountID: before.ID,
		Name:              &name,
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

func TestUpdateEnablingLiveAccountFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "empty encryption key", err: ErrLiveCredentialAccess},
		{name: "short encryption key", err: ErrLiveCredentialAccess},
		{name: "old checked in key", err: ErrLiveCredentialAccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			value := validAccount()
			value.ExecutionMode = exchange.ExecutionModeLive
			value.Status = exchange.AccountStatusDisabled
			store.accounts[value.ID] = value
			status := exchange.AccountStatusEnabled
			service := Service{
				Store: store,
				Secrets: &fakeSecrets{
					secrets:    []ExchangeSecret{validSecret()},
					liveKeyErr: tt.err,
				},
			}

			_, err := service.Update(context.Background(), UpdateCommand{
				ExchangeAccountID: value.ID,
				Status:            &status,
			})
			if !errors.Is(err, ErrLiveCredentialAccess) {
				t.Fatalf("Update() error = %v, want ErrLiveCredentialAccess", err)
			}
			if store.updateCalls != 0 {
				t.Fatalf("Update() writes = %d, want 0", store.updateCalls)
			}
		})
	}
}

func TestExecutionEligibilityUsesCurrentSessionState(t *testing.T) {
	tests := []struct {
		name    string
		status  exchange.AccountStatus
		paused  bool
		ready   bool
		wantErr bool
	}{
		{name: "enabled ready", status: exchange.AccountStatusEnabled, ready: true},
		{name: "disabled", status: exchange.AccountStatusDisabled, ready: true, wantErr: true},
		{name: "paused", status: exchange.AccountStatusEnabled, paused: true, ready: true, wantErr: true},
		{name: "not ready", status: exchange.AccountStatusEnabled, ready: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			value := validAccount()
			value.Status = tt.status
			value.Paused = tt.paused
			if tt.paused {
				value.PauseReason = "manual"
			}
			store.accounts[value.ID] = value
			service := Service{
				Store:        store,
				SessionState: fakeSessionState{ready: tt.ready},
			}

			_, err := service.ExecutionEligibility(context.Background(), value.ID)
			if tt.wantErr && !errors.Is(err, exchangeaccount.ErrAccountNotExecutable) {
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
	if err := service.Pause(context.Background(), value.ID, true, "manual"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if store.pauseCalls != 1 || store.updateCalls != 0 {
		t.Fatalf("pause writes=%d aggregate writes=%d", store.pauseCalls, store.updateCalls)
	}
}

func validAccount() exchangeaccount.Account {
	return exchangeaccount.Account{
		ID:                 "account-1",
		SpaceID:            "space-1",
		Name:               "main",
		Exchange:           exchange.ExchangeBinance,
		MarketType:         exchange.MarketTypeSpot,
		ExecutionMode:      exchange.ExecutionModePaper,
		CredentialSecretID: "secret-1",
		SettlementAsset:    "USDT",
		Status:             exchange.AccountStatusEnabled,
	}
}

func validSwapAccount() exchangeaccount.Account {
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
	accounts      map[string]exchangeaccount.Account
	nameIndex     map[string]string
	createCalls   int
	updateCalls   int
	leverageCalls int
	pauseCalls    int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		accounts:  make(map[string]exchangeaccount.Account),
		nameIndex: make(map[string]string),
	}
}

func (s *memoryStore) Create(_ context.Context, value exchangeaccount.Account) error {
	s.createCalls++
	key := value.SpaceID + "\x00" + value.Name
	if _, found := s.nameIndex[key]; found {
		return ErrAccountConflict
	}
	s.accounts[value.ID] = value
	s.nameIndex[key] = value.ID
	return nil
}

func (s *memoryStore) Get(_ context.Context, id string) (exchangeaccount.Account, error) {
	value, found := s.accounts[id]
	if !found {
		return exchangeaccount.Account{}, ErrAccountNotFound
	}
	return value, nil
}

func (s *memoryStore) Update(_ context.Context, command UpdateCommand) error {
	s.updateCalls++
	value, found := s.accounts[command.ExchangeAccountID]
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

func (s *memoryStore) SetPause(
	_ context.Context,
	id string,
	paused bool,
	reason string,
) error {
	s.pauseCalls++
	value, found := s.accounts[id]
	if !found {
		return ErrAccountNotFound
	}
	value.Paused = paused
	value.PauseReason = reason
	s.accounts[id] = value
	return nil
}

type fakeSecrets struct {
	secrets    []ExchangeSecret
	liveKeyErr error
	listCalls  int
}

func (f *fakeSecrets) ListExchangeSecrets(
	_ context.Context,
	_ exchange.Exchange,
) ([]ExchangeSecret, error) {
	f.listCalls++
	return f.secrets, nil
}

func (f *fakeSecrets) ValidateLiveCredentialAccess() error {
	if f.liveKeyErr != nil {
		return f.liveKeyErr
	}
	return nil
}

type fakeSessionState struct{ ready bool }

func (f fakeSessionState) Ready(string) bool { return f.ready }
