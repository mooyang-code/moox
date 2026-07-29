package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrAccountNotFound      = errors.New("trade account: not found")
	ErrAccountConflict      = errors.New("trade account: conflict")
	ErrInvalidCredential    = errors.New("trade account: invalid Exchange credential")
	ErrLiveCredentialAccess = errors.New("trade account: live credential access is not configured")
	ErrServiceNotConfigured = errors.New("trade account: service is not configured")
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

type SyncExchangeAccountsOptions struct {
	UserID     string
	Exchange   exchange.Exchange
	MarketType exchange.MarketType
}

type Store interface {
	Create(context.Context, exchangeaccount.Account) error
	Get(context.Context, string) (exchangeaccount.Account, error)
	Update(context.Context, UpdateCommand) error
	SetLeverage(context.Context, string, string, shared.Decimal) error
}

type SecretSource interface {
	ListExchangeSecrets(context.Context, exchange.Exchange) ([]ExchangeSecret, error)
	ValidateLiveCredentialAccess() error
}

type SessionState interface {
	ReadyFor(exchangeaccount.Account) bool
	Invalidate(exchangeAccountID string)
}

type Service struct {
	Store        Store
	Secrets      SecretSource
	SessionState SessionState
}

type UpdateCommand struct {
	ExchangeAccountID  string
	Name               *string
	CredentialSecretID *string
	SettlementAsset    *string
	MarginMode         *exchange.MarginMode
	Status             *exchange.AccountStatus
	SyncSymbols        *[]string
}

func (s *Service) Create(
	ctx context.Context,
	value exchangeaccount.Account,
) (exchangeaccount.Account, error) {
	if s == nil || s.Store == nil {
		return exchangeaccount.Account{}, ErrServiceNotConfigured
	}
	if err := value.Validate(); err != nil {
		return exchangeaccount.Account{}, err
	}
	if err := s.validateCredential(ctx, value); err != nil {
		return exchangeaccount.Account{}, err
	}
	if err := s.Store.Create(ctx, value); err != nil {
		return exchangeaccount.Account{}, err
	}
	return value, nil
}

func (s *Service) Update(
	ctx context.Context,
	command UpdateCommand,
) (exchangeaccount.Account, error) {
	if s == nil || s.Store == nil {
		return exchangeaccount.Account{}, ErrServiceNotConfigured
	}
	if strings.TrimSpace(command.ExchangeAccountID) == "" {
		return exchangeaccount.Account{}, exchangeaccount.ErrInvalidAccount
	}
	current, err := s.Store.Get(ctx, command.ExchangeAccountID)
	if err != nil {
		return exchangeaccount.Account{}, err
	}
	projected := current
	applyUpdate(&projected, command)
	if err := projected.Validate(); err != nil {
		return exchangeaccount.Account{}, err
	}
	if command.CredentialSecretID != nil ||
		(command.Status != nil && *command.Status == exchange.AccountStatusEnabled) {
		if err := s.validateCredential(ctx, projected); err != nil {
			return exchangeaccount.Account{}, err
		}
	}
	if err := s.Store.Update(ctx, command); err != nil {
		return exchangeaccount.Account{}, err
	}
	if s.SessionState != nil {
		s.SessionState.Invalidate(command.ExchangeAccountID)
	}
	return s.Store.Get(ctx, command.ExchangeAccountID)
}

func (s *Service) SetLeverage(
	ctx context.Context,
	exchangeAccountID string,
	symbol string,
	leverage shared.Decimal,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceNotConfigured
	}
	if strings.TrimSpace(exchangeAccountID) == "" ||
		strings.TrimSpace(symbol) == "" ||
		leverage.Cmp(shared.Zero()) <= 0 {
		return exchangeaccount.ErrInvalidAccount
	}
	current, err := s.Store.Get(ctx, exchangeAccountID)
	if err != nil {
		return err
	}
	if current.MarketType != exchange.MarketTypeSwap {
		return exchangeaccount.ErrInvalidAccount
	}
	if err := s.Store.SetLeverage(ctx, exchangeAccountID, symbol, leverage); err != nil {
		return err
	}
	if s.SessionState != nil {
		s.SessionState.Invalidate(exchangeAccountID)
	}
	return nil
}

func (s *Service) ExecutionEligibility(
	ctx context.Context,
	exchangeAccountID string,
) (exchangeaccount.Account, error) {
	if s == nil || s.Store == nil {
		return exchangeaccount.Account{}, ErrServiceNotConfigured
	}
	if s.SessionState == nil {
		return exchangeaccount.Account{}, ErrServiceNotConfigured
	}
	current, err := s.Store.Get(ctx, exchangeAccountID)
	if err != nil {
		return exchangeaccount.Account{}, err
	}
	current.Ready = s.SessionState.ReadyFor(current)
	if err := current.ExecutionEligibility(); err != nil {
		return exchangeaccount.Account{}, err
	}
	return current, nil
}

func (s *Service) validateCredential(
	ctx context.Context,
	value exchangeaccount.Account,
) error {
	if value.ExecutionMode == exchange.ExecutionModePaper {
		return nil
	}
	if s.Secrets == nil {
		return ErrServiceNotConfigured
	}
	if err := s.Secrets.ValidateLiveCredentialAccess(); err != nil {
		return fmt.Errorf("%w: %v", ErrLiveCredentialAccess, err)
	}
	secrets, err := s.Secrets.ListExchangeSecrets(ctx, value.Exchange)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if secret.SecretID != value.CredentialSecretID {
			continue
		}
		if secret.Category != "exchange" ||
			secret.Exchange != value.Exchange ||
			secret.Status != "active" {
			return ErrInvalidCredential
		}
		return nil
	}
	return fmt.Errorf("%w: secret %q not found", ErrInvalidCredential, value.CredentialSecretID)
}

func applyUpdate(value *exchangeaccount.Account, command UpdateCommand) {
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
