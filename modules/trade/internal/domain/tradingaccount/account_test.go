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
			name: "LIVE accepts TESTNET",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.ExecutionMode = exchange.ExecutionModeLive
				account.Environment = exchange.AccountEnvironmentTestnet
			}),
		},
		{
			name: "LIVE accepts PRODUCTION",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.ExecutionMode = exchange.ExecutionModeLive
				account.Environment = exchange.AccountEnvironmentProduction
			}),
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
			name: "unsupported Exchange",
			account: mutate(validSpotAccount(), func(account *Account) {
				account.Exchange = exchange.Exchange("OTHER")
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
		MarketType:         exchange.MarketTypeSpot,
		ExecutionMode:      exchange.ExecutionModePaper,
		Environment:        exchange.AccountEnvironmentPaper,
		CredentialSecretID: "secret-1",
		SettlementAsset:    "USDT",
		Status:             exchange.AccountStatusEnabled,
		Ready:              true,
	}
}

func validSwapAccount() Account {
	account := validSpotAccount()
	account.MarketType = exchange.MarketTypeSwap
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
