package tradingaccount

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrInvalidAccount       = errors.New("trade: invalid trading account")
	ErrAccountNotExecutable = errors.New("trade: trading account is not executable")
)

type LiveConfig struct {
	Environment        exchange.AccountEnvironment
	CredentialSecretID string
}

type PaperConfig struct {
	InitialBalance shared.Decimal
	MakerFeeRate   shared.Decimal
	TakerFeeRate   shared.Decimal
	SlippageBPS    shared.Decimal
}

type Account struct {
	ID            string
	SpaceID       string
	Name          string
	Exchange      exchange.Exchange
	MarketType    exchange.MarketType
	ExecutionMode exchange.ExecutionMode
	Live          *LiveConfig
	Paper         *PaperConfig
	// Environment and CredentialSecretID are retained as in-memory projections
	// for existing exchange adapters; persistence uses Live/Paper configuration.
	Environment        exchange.AccountEnvironment
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         exchange.MarginMode
	Status             exchange.AccountStatus
	Ready              bool
	SyncSymbols        []string
	LeverageSettings   map[string]shared.Decimal
	Snapshot           exchange.AccountSnapshot
	SnapshotSourceTime time.Time
	LastSyncAt         time.Time
	LastReadyAt        time.Time
	LastError          string
}

func (a Account) Validate() error {
	if blank(a.ID) ||
		blank(a.SpaceID) ||
		blank(a.Name) ||
		!a.Exchange.Valid() ||
		!a.MarketType.Valid() ||
		!a.ExecutionMode.Valid() ||
		blank(a.SettlementAsset) ||
		!validStatus(a.Status) {
		return invalidAccount("missing or unsupported required field")
	}
	if a.Live == nil && a.ExecutionMode == exchange.ExecutionModeLive && a.Environment.ValidLive() && a.CredentialSecretID != "" {
		a.Live = &LiveConfig{Environment: a.Environment, CredentialSecretID: a.CredentialSecretID}
	}
	if a.Paper == nil && a.ExecutionMode == exchange.ExecutionModePaper && a.Environment == exchange.AccountEnvironmentPaper {
		a.Paper = &PaperConfig{InitialBalance: shared.MustDecimal("1"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero()}
	}
	if (a.Live == nil) == (a.Paper == nil) {
		return invalidAccount("exactly one of LIVE and PAPER config is required")
	}
	if a.ExecutionMode == exchange.ExecutionModeLive {
		if a.Live == nil || !a.Live.Environment.ValidLive() || blank(a.Live.CredentialSecretID) {
			return invalidAccount("LIVE requires environment and credential")
		}
	} else if a.Paper == nil && a.Environment == exchange.AccountEnvironmentPaper {
		a.Paper = &PaperConfig{InitialBalance: shared.MustDecimal("1"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero()}
	} else if a.Paper == nil {
		return invalidAccount("PAPER requires paper config")
	} else if a.Paper.InitialBalance.Cmp(shared.Zero()) <= 0 || a.Paper.MakerFeeRate.Cmp(shared.Zero()) < 0 || a.Paper.TakerFeeRate.Cmp(shared.Zero()) < 0 || a.Paper.SlippageBPS.Cmp(shared.Zero()) < 0 || a.Paper.SlippageBPS.Cmp(shared.MustDecimal("10000")) >= 0 {
		return invalidAccount("invalid paper config")
	}
	switch a.MarketType {
	case exchange.MarketTypeSpot:
		if a.MarginMode != exchange.MarginModeUnspecified || len(a.LeverageSettings) != 0 {
			return invalidAccount("SPOT cannot configure margin mode or leverage")
		}
	case exchange.MarketTypeSwap:
		if a.MarginMode != exchange.MarginModeCross {
			return invalidAccount("SWAP requires CROSS margin mode")
		}
		for symbol, leverage := range a.LeverageSettings {
			if blank(symbol) || leverage.Cmp(shared.Zero()) <= 0 {
				return invalidAccount("SWAP leverage must be positive and symbol-bound")
			}
		}
	}
	seenSymbols := make(map[string]struct{}, len(a.SyncSymbols))
	for _, symbol := range a.SyncSymbols {
		if blank(symbol) {
			return invalidAccount("sync symbol cannot be empty")
		}
		if _, found := seenSymbols[symbol]; found {
			return invalidAccount("sync symbols must be unique")
		}
		seenSymbols[symbol] = struct{}{}
	}
	return nil
}

func (a Account) ExecutionEligibility() error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.ExecutionMode == exchange.ExecutionModeLive &&
		a.MarketType == exchange.MarketTypeSpot &&
		len(a.SyncSymbols) == 0 {
		return ErrAccountNotExecutable
	}
	if a.Status != exchange.AccountStatusEnabled || !a.Ready {
		return ErrAccountNotExecutable
	}
	return nil
}

func (a Account) MarketDataEnvironment() exchange.AccountEnvironment {
	if a.Paper != nil {
		return exchange.AccountEnvironmentProduction
	}
	if a.Live != nil {
		return a.Live.Environment
	}
	return a.Environment
}

func validStatus(status exchange.AccountStatus) bool {
	return status == exchange.AccountStatusEnabled ||
		status == exchange.AccountStatusDisabled ||
		status == exchange.AccountStatusError
}

func invalidAccount(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAccount, reason)
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
