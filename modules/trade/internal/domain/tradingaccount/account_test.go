package tradingaccount

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestAccountValidation(t *testing.T) {
	tests := []struct {
		name    string
		account Account
		wantErr bool
	}{
		{name: "valid SPOT", account: validSpotAccount()},
		{name: "valid SWAP", account: validSwapAccount()},
		{
			name: "PAPER does not require credential",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.CredentialSecretID = ""
			}),
		},
		{
			name: "LIVE requires credential",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.ExecutionMode = exchange.ExecutionModeLive
				account.Environment = exchange.AccountEnvironmentTestnet
				account.CredentialSecretID = ""
				account.Paper = nil
				account.Live = &LiveConfig{Environment: exchange.AccountEnvironmentTestnet}
			}),
			wantErr: true,
		},
		{
			name: "LIVE rejects missing LiveConfig even when legacy fields are set",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.ExecutionMode = exchange.ExecutionModeLive
				account.Environment = exchange.AccountEnvironmentTestnet
				account.CredentialSecretID = "secret-1"
				account.Paper = nil
				account.Live = nil
			}),
			wantErr: true,
		},
		{
			name: "PAPER rejects missing PaperConfig even when environment is PAPER",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Paper = nil
			}),
			wantErr: true,
		},
		{
			name: "PAPER requires PAPER environment",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Environment = exchange.AccountEnvironmentTestnet
			}),
			wantErr: true,
		},
		{
			name: "PAPER accepts empty environment with PaperConfig",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Environment = exchange.AccountEnvironmentUnspecified
			}),
		},
		{
			name: "PAPER rejects nonpositive initial balance",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Paper = &PaperConfig{InitialBalance: shared.Zero(), OrderRetentionDays: DefaultOrderRetentionDays}
			}),
			wantErr: true,
		},
		{
			name: "PAPER rejects invalid slippage",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Paper = &PaperConfig{
					InitialBalance: shared.MustDecimal("100"), SlippageBPS: shared.MustDecimal("10000"),
					OrderRetentionDays: DefaultOrderRetentionDays,
				}
			}),
			wantErr: true,
		},
		{
			name: "rejects both Live and Paper configs",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Live = &LiveConfig{Environment: exchange.AccountEnvironmentTestnet, CredentialSecretID: "secret-1"}
			}),
			wantErr: true,
		},
		{
			name: "LIVE rejects PaperConfig",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.ExecutionMode = exchange.ExecutionModeLive
			}),
			wantErr: true,
		},
		{
			name: "PAPER rejects LiveConfig",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Paper = nil
				account.Live = &LiveConfig{Environment: exchange.AccountEnvironmentTestnet, CredentialSecretID: "secret-1"}
			}),
			wantErr: true,
		},
		{
			name: "LIVE rejects mismatched Environment",
			account: mutate(validLiveAccount(exchange.AccountEnvironmentTestnet), func(account *Account) {
				account.Environment = exchange.AccountEnvironmentProduction
			}),
			wantErr: true,
		},
		{
			name: "LIVE rejects mismatched CredentialSecretID",
			account: mutate(validLiveAccount(exchange.AccountEnvironmentTestnet), func(account *Account) {
				account.CredentialSecretID = "secret-other"
			}),
			wantErr: true,
		},
		{
			name: "LIVE accepts LiveConfig with empty legacy fields",
			account: mutate(validLiveAccount(exchange.AccountEnvironmentProduction), func(account *Account) {
				account.Environment = exchange.AccountEnvironmentUnspecified
				account.CredentialSecretID = ""
			}),
		},
		{
			name:    "LIVE accepts TESTNET",
			account: validLiveAccount(exchange.AccountEnvironmentTestnet),
		},
		{
			name:    "LIVE accepts PRODUCTION",
			account: validLiveAccount(exchange.AccountEnvironmentProduction),
		},
		{
			name: "SPOT rejects margin mode",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.MarginMode = exchange.MarginModeCross
			}),
			wantErr: true,
		},
		{
			name: "SPOT rejects leverage",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.LeverageSettings = map[string]shared.Decimal{
					"BTC-USDT": shared.MustDecimal("5"),
				}
			}),
			wantErr: true,
		},
		{
			name: "SWAP requires CROSS",
			account: mutate(validSwapAccount(), func(account *Account) {
				account.MarginMode = exchange.MarginModeUnspecified
			}),
			wantErr: true,
		},
		{
			name: "SWAP rejects nonpositive leverage",
			account: mutate(validSwapAccount(), func(account *Account) {
				account.LeverageSettings["BTC-USDT"] = shared.Zero()
			}),
			wantErr: true,
		},
		{
			name: "missing identity",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.SpaceID = ""
			}),
			wantErr: true,
		},
		{
			name: "missing Exchange",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Exchange = exchange.ExchangeUnspecified
			}),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.account.Validate()
			if tt.wantErr && !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("Validate() error = %v, want ErrInvalidAccount", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestAccountExecutionEligibility(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Account)
	}{
		{"disabled", func(account *Account) { account.Status = exchange.AccountStatusDisabled }},
		{"not ready", func(account *Account) { account.Ready = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := validSpotAccount()
			tt.mutate(&account)
			if !errors.Is(account.ExecutionEligibility(), ErrAccountNotExecutable) {
				t.Fatalf("ExecutionEligibility() error = %v", account.ExecutionEligibility())
			}
		})
	}

	account := validSpotAccount()
	if err := account.ExecutionEligibility(); err != nil {
		t.Fatalf("ExecutionEligibility() error = %v", err)
	}
}

func validSpotAccount() Account {
	return Account{
		ID:                 "account-1",
		SpaceID:            "space-1",
		Name:               "main",
		Exchange:           exchange.ExchangeBinance,
		Markets:            []exchange.MarketType{exchange.MarketTypeSpot},
		ExecutionMode:      exchange.ExecutionModePaper,
		Environment:        exchange.AccountEnvironmentPaper,
		CredentialSecretID: "secret-1",
		Paper: &PaperConfig{
			InitialBalance:     shared.MustDecimal("100000"),
			MakerFeeRate:       shared.Zero(),
			TakerFeeRate:       shared.Zero(),
			SlippageBPS:        shared.Zero(),
			OrderRetentionDays: DefaultOrderRetentionDays,
		},
		SettlementAsset: "USDT",
		Status:          exchange.AccountStatusEnabled,
		Ready:           true,
	}
}

func validLiveAccount(env exchange.AccountEnvironment) Account {
	account := validSpotAccount()
	account.ExecutionMode = exchange.ExecutionModeLive
	account.Environment = env
	account.CredentialSecretID = "secret-1"
	account.Paper = nil
	account.Live = &LiveConfig{Environment: env, CredentialSecretID: "secret-1"}
	return account
}

func validSwapAccount() Account {
	account := validSpotAccount()
	account.Markets = []exchange.MarketType{exchange.MarketTypeSwap}
	account.MarginMode = exchange.MarginModeCross
	account.LeverageSettings = map[string]shared.Decimal{
		"BTC-USDT": shared.MustDecimal("5"),
	}
	return account
}

func mutate(account Account, fn func(*Account)) Account {
	fn(&account)
	return account
}
