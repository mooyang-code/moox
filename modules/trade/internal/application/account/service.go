package account

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrAccountNotFound       = errors.New("trade account: not found")
	ErrAccountConflict       = errors.New("trade account: conflict")
	ErrInvalidCredential     = errors.New("trade account: invalid Exchange credential")
	ErrLiveTradingDisabled   = errors.New("trade account: production trading is disabled")
	ErrPaperAccountImmutable = errors.New("trade account: paper account configuration is immutable")
	ErrServiceNotConfigured  = errors.New("trade account: service is not configured")
)

type ExchangeSecret struct {
	SecretID    string
	Name        string
	Description string
	Category    string
	Exchange    exchange.Exchange
	Status      string
	KeyID       string
	SecretValue string
	ExtraConfig string
}

type SyncTradingAccountsOptions struct {
	UserID     string
	Exchange   exchange.Exchange
	MarketType exchange.MarketType
}

type Store interface {
	Create(context.Context, tradingaccount.Account) error
	Get(context.Context, string) (tradingaccount.Account, error)
	Update(context.Context, UpdateCommand) error
	SetLeverage(context.Context, string, string, shared.Decimal) error
}

type SecretSource interface {
	GetExchangeSecret(context.Context, string) (ExchangeSecret, error)
}

type SessionState interface {
	ReadyFor(tradingaccount.Account) bool
	Invalidate(tradingAccountID string)
}

type Service struct {
	Store              Store
	Secrets            SecretSource
	SessionState       SessionState
	LiveTradingEnabled bool
}

type UpdateCommand struct {
	TradingAccountID   string
	Name               *string
	CredentialSecretID *string
	SettlementAsset    *string
	MarginMode         *exchange.MarginMode
	Status             *exchange.AccountStatus
	SyncSymbols        *[]string
}

func (s *Service) Create(
	ctx context.Context,
	value tradingaccount.Account,
) (tradingaccount.Account, error) {
	if s == nil || s.Store == nil {
		return tradingaccount.Account{}, ErrServiceNotConfigured
	}
	if err := value.Validate(); err != nil {
		return tradingaccount.Account{}, err
	}
	if value.ExecutionMode == exchange.ExecutionModeLive &&
		value.Environment == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return tradingaccount.Account{}, ErrLiveTradingDisabled
	}
	if err := s.validateCredential(ctx, value); err != nil {
		return tradingaccount.Account{}, err
	}
	if err := s.Store.Create(ctx, value); err != nil {
		return tradingaccount.Account{}, err
	}
	return value, nil
}

func (s *Service) Update(
	ctx context.Context,
	command UpdateCommand,
) (tradingaccount.Account, error) {
	if s == nil || s.Store == nil {
		return tradingaccount.Account{}, ErrServiceNotConfigured
	}
	if strings.TrimSpace(command.TradingAccountID) == "" {
		return tradingaccount.Account{}, tradingaccount.ErrInvalidAccount
	}
	current, err := s.Store.Get(ctx, command.TradingAccountID)
	if err != nil {
		return tradingaccount.Account{}, err
	}
	if current.ExecutionMode == exchange.ExecutionModePaper &&
		(command.Name != nil || command.CredentialSecretID != nil ||
			command.SettlementAsset != nil || command.MarginMode != nil ||
			command.Status != nil || command.SyncSymbols != nil) {
		return tradingaccount.Account{}, ErrPaperAccountImmutable
	}
	projected := current
	applyUpdate(&projected, command)
	if err := projected.Validate(); err != nil {
		return tradingaccount.Account{}, err
	}
	if current.Status != exchange.AccountStatusEnabled &&
		projected.Status == exchange.AccountStatusEnabled &&
		projected.ExecutionMode == exchange.ExecutionModeLive &&
		projected.Environment == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return tradingaccount.Account{}, ErrLiveTradingDisabled
	}
	if command.CredentialSecretID != nil ||
		(command.Status != nil && *command.Status == exchange.AccountStatusEnabled) {
		if err := s.validateCredential(ctx, projected); err != nil {
			return tradingaccount.Account{}, err
		}
	}
	if err := s.Store.Update(ctx, command); err != nil {
		return tradingaccount.Account{}, err
	}
	if s.SessionState != nil {
		s.SessionState.Invalidate(command.TradingAccountID)
	}
	return s.Store.Get(ctx, command.TradingAccountID)
}

func (s *Service) SetLeverage(
	ctx context.Context,
	tradingAccountID string,
	symbol string,
	leverage shared.Decimal,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceNotConfigured
	}
	if strings.TrimSpace(tradingAccountID) == "" ||
		strings.TrimSpace(symbol) == "" ||
		leverage.Cmp(shared.Zero()) <= 0 {
		return tradingaccount.ErrInvalidAccount
	}
	current, err := s.Store.Get(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	if current.MarketType != exchange.MarketTypeSwap {
		return tradingaccount.ErrInvalidAccount
	}
	if current.ExecutionMode == exchange.ExecutionModePaper {
		return ErrPaperAccountImmutable
	}
	if err := s.Store.SetLeverage(ctx, tradingAccountID, symbol, leverage); err != nil {
		return err
	}
	if s.SessionState != nil {
		s.SessionState.Invalidate(tradingAccountID)
	}
	return nil
}

func (s *Service) ExecutionEligibility(
	ctx context.Context,
	tradingAccountID string,
) (tradingaccount.Account, error) {
	if s == nil || s.Store == nil {
		return tradingaccount.Account{}, ErrServiceNotConfigured
	}
	if s.SessionState == nil {
		return tradingaccount.Account{}, ErrServiceNotConfigured
	}
	current, err := s.Store.Get(ctx, tradingAccountID)
	if err != nil {
		return tradingaccount.Account{}, err
	}
	current.Ready = s.SessionState.ReadyFor(current)
	if err := current.ExecutionEligibility(); err != nil {
		return tradingaccount.Account{}, err
	}
	if current.Environment == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return tradingaccount.Account{}, ErrLiveTradingDisabled
	}
	return current, nil
}

func (s *Service) validateCredential(
	ctx context.Context,
	value tradingaccount.Account,
) error {
	if value.ExecutionMode == exchange.ExecutionModePaper {
		return nil
	}
	if s.Secrets == nil {
		return ErrServiceNotConfigured
	}
	secret, err := s.Secrets.GetExchangeSecret(ctx, value.CredentialSecretID)
	if err != nil {
		return err
	}
	if secret.SecretID != value.CredentialSecretID ||
		secret.Category != "exchange" ||
		secret.Exchange != value.Exchange ||
		secret.Status != "active" {
		return ErrInvalidCredential
	}
	return nil
}

func applyUpdate(value *tradingaccount.Account, command UpdateCommand) {
	if command.Name != nil {
		value.Name = *command.Name
	}
	if command.CredentialSecretID != nil {
		value.CredentialSecretID = *command.CredentialSecretID
	}
	if command.SettlementAsset != nil {
		value.SettlementAsset = *command.SettlementAsset
	}
	if command.MarginMode != nil {
		value.MarginMode = *command.MarginMode
	}
	if command.Status != nil {
		value.Status = *command.Status
	}
	if command.SyncSymbols != nil {
		value.SyncSymbols = append([]string(nil), (*command.SyncSymbols)...)
	}
}
