package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var (
	ErrAccountNotFound       = errors.New("交易账户不存在")
	ErrAccountConflict       = errors.New("交易账户已存在或正被修改")
	ErrInvalidCredential     = errors.New("交易所密钥不正确")
	ErrLiveTradingDisabled   = errors.New("生产环境实盘交易未开启")
	ErrPaperAccountImmutable = errors.New("模拟账户的名称、杠杆、结算资产与重新启用等配置创建后不能修改（仅支持关闭账户或修改订单保留天数）；要换参数请新建一个模拟账户")
	ErrServiceNotConfigured  = errors.New("交易账户服务未配置")
	// ErrOrderRetentionPaperOnly：订单保留天数是模拟账户的配置，实盘账户的订单与成交不清理。
	ErrOrderRetentionPaperOnly = errors.New("订单保留天数只有模拟账户能设置，实盘账户的订单与成交不清理")
	ErrInvalidOrderRetention   = fmt.Errorf("订单保留天数要在 0～%d 之间（0 表示不清理）", tradingaccount.MaxOrderRetentionDays)
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

type SecretSource interface {
	GetExchangeSecret(context.Context, string) (ExchangeSecret, error)
}

// SymbolConfigs 记录会话里已经在交易所设置过的标的。
type SymbolConfigs interface {
	ForgetSymbolConfig(tradingAccountID string, symbol exchange.SymbolKey)
	ForgetAllSymbolConfigs(tradingAccountID string)
}

type SessionState interface {
	ReadyFor(store.TradingAccountRecord) bool
}

type Service struct {
	Store              *store.Store
	NewID              func() string
	Secrets            SecretSource
	SessionState       SessionState
	SymbolConfigs      SymbolConfigs
	Exchanges          execution.ExchangeCatalog
	LiveTradingEnabled bool
	// Inspector 检测实盘密钥的账户类型和权限；实盘账户建立、换密钥、重新启用前必须检测通过。
	Inspector Inspector
	Now       func() time.Time
}

type UpdateCommand struct {
	TradingAccountID   string
	Name               *string
	CredentialSecretID *string
	SettlementAsset    *string
	MarginMode         *exchange.MarginMode
	Status             *exchange.AccountStatus
	// OrderRetentionDays 是模拟账户的订单保留天数（0 表示不清理），模拟账户创建后唯一能改的配置；实盘账户给了就拒绝。
	OrderRetentionDays *int
	// AccountMode、InspectionJSON 只由检测写入（换密钥或重新启用时），调用方不用设置。
	AccountMode    *exchange.AccountMode
	InspectionJSON *string
}

func (s *Service) Create(
	ctx context.Context,
	value store.TradingAccountRecord,
) (store.TradingAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.TradingAccountRecord{}, ErrServiceNotConfigured
	}
	// 场所校验在前：A 股实盘先报“QMT 执行端尚未接入”，不让用户先碰到凭据、检测之类的错误。
	if err := execution.ValidateAccount(
		s.Exchanges,
		exchange.Exchange(value.Exchange),
		value.MarketTypes(),
		value.SettlementAsset,
		exchange.ExecutionMode(value.ExecutionMode),
	); err != nil {
		return store.TradingAccountRecord{}, fmt.Errorf("%w: %v", tradingaccount.ErrInvalidAccount, err)
	}
	if err := value.Validate(); err != nil {
		return store.TradingAccountRecord{}, err
	}
	if value.ExecutionMode == string(exchange.ExecutionModeLive) &&
		value.EffectiveEnvironment() == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return store.TradingAccountRecord{}, ErrLiveTradingDisabled
	}
	if err := s.validateCredential(ctx, value); err != nil {
		return store.TradingAccountRecord{}, err
	}
	if err := s.rejectDuplicateCredential(ctx, value); err != nil {
		return store.TradingAccountRecord{}, err
	}
	// 必须先检测通过：账户类型和权限靠检测得到，不靠手填。
	value, err := s.inspectForSave(ctx, value)
	if err != nil {
		return store.TradingAccountRecord{}, err
	}
	if value.ExecutionMode == string(exchange.ExecutionModePaper) {
		value.Environment = string(exchange.AccountEnvironmentPaper)
		if value.PaperConfig != nil {
			cfg := *value.PaperConfig
			cfg.SpaceID, cfg.TradingAccountID = value.SpaceID, value.TradingAccountID
			value.PaperConfig = &cfg
		}
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(value)
	}); err != nil {
		return store.TradingAccountRecord{}, err
	}
	return s.getAccount(ctx, value.TradingAccountID)
}

func (s *Service) Update(
	ctx context.Context,
	command UpdateCommand,
) (store.TradingAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.TradingAccountRecord{}, ErrServiceNotConfigured
	}
	if strings.TrimSpace(command.TradingAccountID) == "" {
		return store.TradingAccountRecord{}, tradingaccount.ErrInvalidAccount
	}
	current, err := s.getAccount(ctx, command.TradingAccountID)
	if err != nil {
		return store.TradingAccountRecord{}, err
	}
	if current.ExecutionMode == string(exchange.ExecutionModePaper) {
		if command.Name != nil || command.CredentialSecretID != nil ||
			command.SettlementAsset != nil || command.MarginMode != nil ||
			(command.Status != nil && *command.Status != exchange.AccountStatusDisabled) {
			return store.TradingAccountRecord{}, ErrPaperAccountImmutable
		}
		closing := command.Status != nil && *command.Status == exchange.AccountStatusDisabled
		if closing && command.OrderRetentionDays != nil {
			return store.TradingAccountRecord{}, fmt.Errorf("%w: 关闭模拟账户时不能同时修改订单保留天数", tradingaccount.ErrInvalidAccount)
		}
		if command.OrderRetentionDays != nil {
			if !tradingaccount.ValidOrderRetentionDays(*command.OrderRetentionDays) {
				return store.TradingAccountRecord{}, ErrInvalidOrderRetention
			}
			unlock, lockErr := s.Store.LockTradingAccountContext(ctx, current.TradingAccountID)
			if lockErr != nil {
				return store.TradingAccountRecord{}, lockErr
			}
			err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
				return tx.SetPaperOrderRetentionDays(current.SpaceID, current.TradingAccountID, *command.OrderRetentionDays)
			})
			unlock()
			if err != nil {
				return store.TradingAccountRecord{}, err
			}
		}
		if closing {
			return s.ClosePaperAccount(ctx, current.SpaceID, current.TradingAccountID)
		}
		return s.getAccount(ctx, command.TradingAccountID)
	}
	if command.OrderRetentionDays != nil {
		return store.TradingAccountRecord{}, ErrOrderRetentionPaperOnly
	}
	projected := current
	applyUpdate(&projected, command)
	if err := projected.Validate(); err != nil {
		return store.TradingAccountRecord{}, err
	}
	if command.SettlementAsset != nil {
		if err := execution.ValidateAccount(
			s.Exchanges,
			exchange.Exchange(projected.Exchange),
			projected.MarketTypes(),
			projected.SettlementAsset,
			exchange.ExecutionMode(projected.ExecutionMode),
		); err != nil {
			return store.TradingAccountRecord{}, fmt.Errorf("%w: %v", tradingaccount.ErrInvalidAccount, err)
		}
	}
	if current.Status == string(exchange.AccountStatusEnabled) && projected.Status != string(exchange.AccountStatusEnabled) {
		name, err := s.logicalAccountName(ctx, current.SpaceID, current.TradingAccountID)
		if err != nil {
			return store.TradingAccountRecord{}, err
		}
		if name != "" {
			return store.TradingAccountRecord{}, fmt.Errorf("%w（组合账户 %s）", ErrAccountInLogicalAccount, name)
		}
	}
	if current.Status != string(exchange.AccountStatusEnabled) &&
		projected.Status == string(exchange.AccountStatusEnabled) &&
		projected.ExecutionMode == string(exchange.ExecutionModeLive) &&
		projected.EffectiveEnvironment() == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return store.TradingAccountRecord{}, ErrLiveTradingDisabled
	}
	if command.CredentialSecretID != nil ||
		(command.Status != nil && *command.Status == exchange.AccountStatusEnabled) {
		if err := s.validateCredential(ctx, projected); err != nil {
			return store.TradingAccountRecord{}, err
		}
		if err := s.rejectDuplicateCredential(ctx, projected); err != nil {
			return store.TradingAccountRecord{}, err
		}
		// 换密钥或重新启用：必须重新检测通过才保存，账户类型随之更新。
		inspected, err := s.inspectForSave(ctx, projected)
		if err != nil {
			return store.TradingAccountRecord{}, err
		}
		if projected.ExecutionMode == string(exchange.ExecutionModeLive) {
			mode := exchange.AccountMode(inspected.AccountMode)
			command.AccountMode, command.InspectionJSON = &mode, &inspected.InspectionJSON
		}
	}
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, command.TradingAccountID)
	if lockErr != nil {
		return store.TradingAccountRecord{}, lockErr
	}
	defer unlock()
	latest, err := s.getAccount(ctx, command.TradingAccountID)
	if err != nil {
		return store.TradingAccountRecord{}, err
	}
	applyUpdate(&latest, command)
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountConfiguration(
			latest.SpaceID,
			latest.TradingAccountID,
			store.TradingAccountConfiguration{
				Name:               latest.Name,
				CredentialSecretID: latest.CredentialSecretID,
				SettlementAsset:    latest.SettlementAsset,
				MarginMode:         latest.MarginMode,
				Status:             latest.Status,
			},
		); err != nil {
			return err
		}
		// 换密钥或重新启用时在同一个事务里写入重新检测的结果，检测通过才保存。
		if command.InspectionJSON != nil {
			return tx.SetTradingAccountInspection(
				latest.SpaceID,
				latest.TradingAccountID,
				latest.CredentialSecretID,
				latest.AccountMode,
				latest.InspectionJSON,
			)
		}
		return nil
	}); err != nil {
		return store.TradingAccountRecord{}, err
	}
	return s.getAccount(ctx, command.TradingAccountID)
}

// rejectDuplicateCredential 保证一把密钥（交易所、环境、密钥 ID）全局只有一个启用的执行账户：
// 两个会话看到同一批订单，会互相当成外部订单，组合账户被自动暂停。
// 账户同时做现货和合约，所以不管市场是否相同都冲突；表上的唯一索引兜底并发。
func (s *Service) rejectDuplicateCredential(ctx context.Context, value store.TradingAccountRecord) error {
	if value.ExecutionMode != string(exchange.ExecutionModeLive) || value.Status != string(exchange.AccountStatusEnabled) {
		return nil
	}
	accounts, err := s.Store.ListEnabledTradingAccounts(ctx)
	if err != nil {
		return err
	}
	for _, other := range accounts {
		if other.TradingAccountID != value.TradingAccountID &&
			other.ExecutionMode == string(exchange.ExecutionModeLive) &&
			other.Exchange == value.Exchange &&
			other.EffectiveEnvironment() == value.EffectiveEnvironment() &&
			other.CredentialSecretID == value.CredentialSecretID {
			return fmt.Errorf("%w: 账户“%s”已在使用这把密钥，一把密钥只能对应一个执行账户", tradingaccount.ErrInvalidAccount, other.Name)
		}
	}
	return nil
}

// SetLeverage 设置杠杆。leverageKey 是内部标的 ID（例如 BTC-USDT-SWAP）或账户默认杠杆的键 "*"；
// exchangeSymbol 是该标的的（市场，交易所代码），用来让会话下次开仓前重新设置；默认杠杆影响全部标的，必须传零值键。
func (s *Service) SetLeverage(
	ctx context.Context,
	tradingAccountID string,
	leverageKey string,
	exchangeSymbol exchange.SymbolKey,
	leverage shared.Decimal,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceNotConfigured
	}
	isDefault := leverageKey == store.DefaultLeverageKey
	if strings.TrimSpace(tradingAccountID) == "" ||
		strings.TrimSpace(leverageKey) == "" ||
		(isDefault && !exchangeSymbol.IsZero()) ||
		(!isDefault && !exchangeSymbol.Valid()) ||
		leverage.Cmp(shared.Zero()) <= 0 {
		return tradingaccount.ErrInvalidAccount
	}
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, tradingAccountID)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	current, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	if !current.HasLeverageMarket() {
		return tradingaccount.ErrInvalidAccount
	}
	if current.ExecutionMode == string(exchange.ExecutionModePaper) {
		return ErrPaperAccountImmutable
	}
	settings := cloneLeverage(current.LeverageSettings)
	settings[leverageKey] = leverage.String()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetTradingAccountLeverage(current.SpaceID, current.TradingAccountID, settings)
	}); err != nil {
		return err
	}
	// 不重启会话，只让该标的下次开仓前重新设置。
	if s.SymbolConfigs != nil {
		if isDefault {
			s.SymbolConfigs.ForgetAllSymbolConfigs(tradingAccountID)
		} else {
			s.SymbolConfigs.ForgetSymbolConfig(tradingAccountID, exchangeSymbol)
		}
	}
	return nil
}

func (s *Service) ExecutionEligibility(
	ctx context.Context,
	tradingAccountID string,
) (store.TradingAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.TradingAccountRecord{}, ErrServiceNotConfigured
	}
	if s.SessionState == nil {
		return store.TradingAccountRecord{}, ErrServiceNotConfigured
	}
	current, err := s.getAccount(ctx, tradingAccountID)
	if err != nil {
		return store.TradingAccountRecord{}, err
	}
	// 持久化的同步状态与当前会话状态必须同时就绪：私有流的持仓/成交更新会把持久化状态置为“等待完整同步”，
	// 此时会话仍然就绪，不能让会话状态把它覆盖回可交易（否则会用尚未同步的资金信息通过准入）。
	current.Ready = current.Ready && s.SessionState.ReadyFor(current)
	if err := current.ExecutionEligibility(); err != nil {
		return store.TradingAccountRecord{}, err
	}
	if current.ExecutionMode == string(exchange.ExecutionModeLive) &&
		current.EffectiveEnvironment() == exchange.AccountEnvironmentProduction &&
		!s.LiveTradingEnabled {
		return store.TradingAccountRecord{}, ErrLiveTradingDisabled
	}
	return current, nil
}

func (s *Service) getAccount(ctx context.Context, id string) (store.TradingAccountRecord, error) {
	record, err := s.Store.GetTradingAccountByID(ctx, id)
	if err != nil {
		return store.TradingAccountRecord{}, err
	}
	if record.ExecutionMode == string(exchange.ExecutionModePaper) && record.PaperConfig == nil {
		if cfg, cfgErr := s.Store.GetPaperAccountConfig(ctx, record.SpaceID, record.TradingAccountID); cfgErr == nil {
			record.PaperConfig = &cfg
		}
	}
	return record, nil
}

func (s *Service) logicalAccountName(ctx context.Context, spaceID, tradingAccountID string) (string, error) {
	logical, _, err := s.Store.FindLogicalAccountMembership(ctx, spaceID, tradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return logical.Name, nil
}

func (s *Service) setInspection(ctx context.Context, id, credentialSecretID string, mode exchange.AccountMode, inspectionJSON string) error {
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	current, err := s.Store.GetTradingAccountByID(ctx, id)
	if err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetTradingAccountInspection(current.SpaceID, current.TradingAccountID, credentialSecretID, string(mode), inspectionJSON)
	})
}

func (s *Service) validateCredential(
	ctx context.Context,
	value store.TradingAccountRecord,
) error {
	if value.ExecutionMode == string(exchange.ExecutionModePaper) {
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
		secret.Exchange != exchange.Exchange(value.Exchange) ||
		secret.Status != "active" {
		return ErrInvalidCredential
	}
	return nil
}

func applyUpdate(value *store.TradingAccountRecord, command UpdateCommand) {
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
		value.MarginMode = string(*command.MarginMode)
	}
	if command.Status != nil {
		value.Status = string(*command.Status)
	}
	if command.OrderRetentionDays != nil && value.PaperConfig != nil {
		paper := *value.PaperConfig
		paper.OrderRetentionDays = *command.OrderRetentionDays
		value.PaperConfig = &paper
	}
	if command.AccountMode != nil {
		value.AccountMode = string(*command.AccountMode)
	}
	if command.InspectionJSON != nil {
		value.InspectionJSON = *command.InspectionJSON
	}
}

func cloneLeverage(values store.LeverageSettings) store.LeverageSettings {
	cloned := make(store.LeverageSettings, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func marketStrings(markets []exchange.MarketType) []string {
	result := make([]string, len(markets))
	for i, market := range markets {
		result[i] = string(market)
	}
	return result
}
