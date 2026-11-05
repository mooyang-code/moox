package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

var (
	ErrAccountNotFound       = errors.New("trade account: not found")
	ErrAccountConflict       = errors.New("trade account: conflict")
	ErrInvalidCredential     = errors.New("trade account: invalid Exchange credential")
	ErrLiveTradingDisabled   = errors.New("trade account: production trading is disabled")
	ErrPaperAccountImmutable = errors.New("trade account: paper account configuration is immutable")
	ErrServiceNotConfigured  = errors.New("trade account: service is not configured")
	// ErrMultiMarketUnsupported：适配器和会话要等改成按标的所属市场工作后才能支持同时开通多个市场的账户。
	ErrMultiMarketUnsupported = errors.New("trade account: 同时开通多个市场的执行账户暂不支持创建")
	// ErrAccountInLogicalAccount：停用后没有会话、同步必然失败，账户就移不出组合了，所以要先移出再停用。
	ErrAccountInLogicalAccount = errors.New("执行账户还在组合账户里，先把它移出组合账户再停用")
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

type Store interface {
	Create(context.Context, tradingaccount.Account) error
	Get(context.Context, string) (tradingaccount.Account, error)
	Update(context.Context, UpdateCommand) error
	SetLeverage(context.Context, string, string, shared.Decimal) error
	// ListEnabled 返回全部启用的执行账户。
	ListEnabled(context.Context) ([]tradingaccount.Account, error)
	// LogicalAccountName 返回执行账户所属组合账户的名称（启用或停用成员都算），不属于任何组合时返回空串。
	LogicalAccountName(ctx context.Context, spaceID, tradingAccountID string) (string, error)
}

type SecretSource interface {
	GetExchangeSecret(context.Context, string) (ExchangeSecret, error)
}

// SymbolConfigs 记录会话里已经在交易所设置过的标的。
type SymbolConfigs interface {
	ForgetSymbolConfig(tradingAccountID, symbol string)
}

type SessionState interface {
	ReadyFor(tradingaccount.Account) bool
}

type Service struct {
	Store              Store
	Secrets            SecretSource
	SessionState       SessionState
	SymbolConfigs      SymbolConfigs
	Exchanges          execution.ExchangeCatalog
	LiveTradingEnabled bool
}

type UpdateCommand struct {
	TradingAccountID   string
	Name               *string
	CredentialSecretID *string
	SettlementAsset    *string
	MarginMode         *exchange.MarginMode
	Status             *exchange.AccountStatus
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
	if value.SoleMarket() == exchange.MarketTypeUnspecified {
		return tradingaccount.Account{}, ErrMultiMarketUnsupported
	}
	if value.ExecutionMode == exchange.ExecutionModeLive &&
		value.Environment == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return tradingaccount.Account{}, ErrLiveTradingDisabled
	}
	if err := s.validateCredential(ctx, value); err != nil {
		return tradingaccount.Account{}, err
	}
	if err := s.rejectDuplicateCredential(ctx, value); err != nil {
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
			command.Status != nil) {
		return tradingaccount.Account{}, ErrPaperAccountImmutable
	}
	projected := current
	applyUpdate(&projected, command)
	if err := projected.Validate(); err != nil {
		return tradingaccount.Account{}, err
	}
	if current.Status == exchange.AccountStatusEnabled && projected.Status != exchange.AccountStatusEnabled {
		name, err := s.Store.LogicalAccountName(ctx, current.SpaceID, current.ID)
		if err != nil {
			return tradingaccount.Account{}, err
		}
		if name != "" {
			return tradingaccount.Account{}, fmt.Errorf("%w（组合账户 %s）", ErrAccountInLogicalAccount, name)
		}
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
		if err := s.rejectDuplicateCredential(ctx, projected); err != nil {
			return tradingaccount.Account{}, err
		}
	}
	if err := s.Store.Update(ctx, command); err != nil {
		return tradingaccount.Account{}, err
	}
	return s.Store.Get(ctx, command.TradingAccountID)
}

// rejectDuplicateCredential 保证一把密钥（交易所、环境、密钥 ID）全局只有一个启用的执行账户：
// 两个会话看到同一批订单，会互相当成外部订单，组合账户被自动暂停。
// 账户同时做现货和合约，所以不管市场是否相同都冲突；表上的唯一索引兜底并发。
func (s *Service) rejectDuplicateCredential(ctx context.Context, value tradingaccount.Account) error {
	if value.ExecutionMode != exchange.ExecutionModeLive || value.Status != exchange.AccountStatusEnabled {
		return nil
	}
	accounts, err := s.Store.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, other := range accounts {
		if other.ID != value.ID && other.ExecutionMode == exchange.ExecutionModeLive &&
			other.Exchange == value.Exchange && other.Environment == value.Environment &&
			other.CredentialSecretID == value.CredentialSecretID {
			return fmt.Errorf("%w: 账户“%s”已在使用这把密钥，一把密钥只能对应一个执行账户", tradingaccount.ErrInvalidAccount, other.Name)
		}
	}
	return nil
}

// SetLeverage 设置杠杆。leverageKey 是内部标的 ID（例如 BTC-USDT-SWAP）或账户默认杠杆的键 "*"；
// exchangeSymbol 是该标的的交易所代码（默认杠杆时同样传 "*"），用来让会话下次开仓前重新设置。
func (s *Service) SetLeverage(
	ctx context.Context,
	tradingAccountID string,
	leverageKey string,
	exchangeSymbol string,
	leverage shared.Decimal,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceNotConfigured
	}
	if strings.TrimSpace(tradingAccountID) == "" ||
		strings.TrimSpace(leverageKey) == "" ||
		strings.TrimSpace(exchangeSymbol) == "" ||
		leverage.Cmp(shared.Zero()) <= 0 {
		return tradingaccount.ErrInvalidAccount
	}
	current, err := s.Store.Get(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	if !current.HasLeverageMarket() {
		return tradingaccount.ErrInvalidAccount
	}
	if current.ExecutionMode == exchange.ExecutionModePaper {
		return ErrPaperAccountImmutable
	}
	if err := s.Store.SetLeverage(ctx, tradingAccountID, leverageKey, leverage); err != nil {
		return err
	}
	// 不重启会话，只让该标的下次开仓前重新设置。
	if s.SymbolConfigs != nil {
		s.SymbolConfigs.ForgetSymbolConfig(tradingAccountID, exchangeSymbol)
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
	// 持久化的同步状态与当前会话状态必须同时就绪：私有流的持仓/成交更新会把持久化状态置为“等待完整同步”，
	// 此时会话仍然就绪，不能让会话状态把它覆盖回可交易（否则会用尚未同步的资金信息通过准入）。
	current.Ready = current.Ready && s.SessionState.ReadyFor(current)
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
		// 实盘账户的凭据同时存在于 Live 配置里，落库读取的是 Live 的值：两处必须一起更新，否则换凭据成功返回却仍保存旧凭据
		if value.Live != nil {
			live := *value.Live
			live.CredentialSecretID = *command.CredentialSecretID
			value.Live = &live
		}
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
}
