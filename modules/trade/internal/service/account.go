package service

import (
	"context"
	"strings"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// AccountService 实现账户域：账户、余额、资金流水、API 凭证。
type AccountService struct {
	store        Store
	secretSource ExchangeSecretSource
	exNew        ExchangeFactory
}

// ---- 账户 ----

// CreateAccount 创建账户。account_id 为空时自动生成。
func (s *AccountService) CreateAccount(ctx context.Context, spaceID string, a *Account) (*Account, error) {
	if a == nil || strings.TrimSpace(a.AccountName) == "" || a.UserID == "" {
		return nil, ErrInvalidParam
	}
	if a.AccountID == "" {
		a.AccountID = genID("acc")
	}
	if a.AccountType == "" {
		a.AccountType = AccountSpot
	}
	if a.BaseCurrency == "" {
		a.BaseCurrency = "USDT"
	}
	if a.Status == 0 {
		a.Status = AccountNormal
	}
	if err := s.store.CreateAccount(ctx, spaceID, a); err != nil {
		return nil, err
	}
	return a, nil
}

// UpdateAccount 更新账户基础信息。
func (s *AccountService) UpdateAccount(ctx context.Context, spaceID string, a *Account) (*Account, error) {
	if a == nil || a.AccountID == "" {
		return nil, ErrInvalidParam
	}
	if err := s.store.UpdateAccount(ctx, spaceID, a); err != nil {
		return nil, err
	}
	return a, nil
}

// DeleteAccount 软删除账户。
func (s *AccountService) DeleteAccount(ctx context.Context, spaceID, accountID string) error {
	if accountID == "" {
		return ErrInvalidParam
	}
	return s.store.DeleteAccount(ctx, spaceID, accountID)
}

// GetAccount 查询单个账户。
func (s *AccountService) GetAccount(ctx context.Context, spaceID, accountID string) (*Account, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	return s.store.GetAccount(ctx, spaceID, accountID)
}

// ListAccounts 分页查询账户。
func (s *AccountService) ListAccounts(ctx context.Context, spaceID string, f AccountFilter, page Page) ([]*Account, int, error) {
	return s.store.ListAccounts(ctx, spaceID, f, page.Normalize())
}

// ---- 余额 ----

// GetBalances 查询账户余额。
func (s *AccountService) GetBalances(ctx context.Context, spaceID, accountID string, currencies []string) ([]*Balance, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	return s.store.GetBalances(ctx, spaceID, accountID, currencies)
}

// UpsertBalances 覆盖写入余额快照（如交易所同步后）。
func (s *AccountService) UpsertBalances(ctx context.Context, spaceID string, balances []*Balance) error {
	if len(balances) == 0 {
		return nil
	}
	return s.store.UpsertBalances(ctx, spaceID, balances)
}

// SyncBalances 从交易所拉取并刷新本地余额快照。
func (s *AccountService) SyncBalances(ctx context.Context, spaceID, accountID string) ([]*Balance, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	account, err := s.store.GetAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, err
	}
	if account.ChannelID == "" {
		return s.store.GetBalances(ctx, spaceID, accountID, nil)
	}
	ch, err := s.store.GetChannel(ctx, spaceID, account.ChannelID)
	if err != nil {
		return nil, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return nil, err
	}
	exBalances, err := adapter.GetBalances(ctx, cred, exchange.MarketType(ch.MarketType), nil)
	if err != nil {
		return nil, err
	}
	domain := make([]*Balance, 0, len(exBalances))
	for _, b := range exBalances {
		domain = append(domain, &Balance{
			AccountID: accountID,
			Currency:  b.Currency,
			Available: b.Available,
			Frozen:    b.Frozen,
			Total:     b.Total,
		})
	}
	if err := s.store.UpsertBalances(ctx, spaceID, domain); err != nil {
		return nil, err
	}
	return s.store.GetBalances(ctx, spaceID, accountID, nil)
}

// ---- 资金流水 ----

// ListFundFlows 分页查询资金流水。
func (s *AccountService) ListFundFlows(ctx context.Context, spaceID string, f FundFlowFilter, page Page) ([]*FundFlow, int, error) {
	if f.AccountID == "" {
		return nil, 0, ErrInvalidParam
	}
	return s.store.ListFundFlows(ctx, spaceID, f, page.Normalize())
}

// Transfer 账户间内部划转：生成成对流水（转出/转入），由 Store 在同一事务内落库并更新余额。
func (s *AccountService) Transfer(ctx context.Context, spaceID string, from, to, currency, amount, remark string) (outFlowID, inFlowID string, err error) {
	if from == "" || to == "" || from == to || currency == "" || amount == "" {
		return "", "", ErrInvalidParam
	}
	outFlowID = genID("flow")
	inFlowID = genID("flow")
	flows := []*FundFlow{
		{FlowID: outFlowID, AccountID: from, Currency: currency, BizType: "transfer_out", Direction: -1, Amount: amount, RefType: "transfer", RefID: inFlowID, Remark: remark},
		{FlowID: inFlowID, AccountID: to, Currency: currency, BizType: "transfer_in", Direction: 1, Amount: amount, RefType: "transfer", RefID: outFlowID, Remark: remark},
	}
	if err = s.store.AppendFundFlows(ctx, spaceID, flows); err != nil {
		return "", "", err
	}
	return outFlowID, inFlowID, nil
}

// ---- API 凭证 ----

// CreateAPIKey 新增 API 凭证。敏感字段由 Store/DAO 层加密落库。
func (s *AccountService) CreateAPIKey(ctx context.Context, spaceID string, k *APIKey) (string, error) {
	if k == nil || k.AccountID == "" || k.Exchange == "" || k.APIKey == "" || k.APISecret == "" {
		return "", ErrInvalidParam
	}
	if k.APIKeyID == "" {
		k.APIKeyID = genID("ak")
	}
	if k.Status == 0 {
		k.Status = 1
	}
	if err := s.store.CreateAPIKey(ctx, spaceID, k); err != nil {
		return "", err
	}
	return k.APIKeyID, nil
}

// DeleteAPIKey 删除 API 凭证。
func (s *AccountService) DeleteAPIKey(ctx context.Context, spaceID, apiKeyID string) error {
	if apiKeyID == "" {
		return ErrInvalidParam
	}
	return s.store.DeleteAPIKey(ctx, spaceID, apiKeyID)
}

// ListAPIKeys 查询账户的 API 凭证（调用方负责脱敏后返回）。
func (s *AccountService) ListAPIKeys(ctx context.Context, spaceID, accountID string) ([]*APIKey, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	return s.store.ListAPIKeys(ctx, spaceID, accountID)
}

// SyncExchangeAccounts 从后台秘钥管理导入交易所账户、API Key 与默认通道。
func (s *AccountService) SyncExchangeAccounts(ctx context.Context, spaceID string, opts SyncExchangeAccountsOptions) ([]*Account, error) {
	if s.secretSource == nil || s.store == nil || opts.UserID == "" {
		return nil, ErrInvalidParam
	}
	provider := strings.TrimSpace(opts.Provider)
	if provider == "" {
		provider = "binance"
	}
	secrets, err := s.secretSource.ListExchangeSecrets(ctx, provider)
	if err != nil {
		return nil, err
	}
	out := make([]*Account, 0, len(secrets))
	for _, sec := range secrets {
		if strings.TrimSpace(sec.SecretID) == "" || strings.TrimSpace(sec.KeyID) == "" || strings.TrimSpace(sec.SecretValue) == "" {
			continue
		}
		cfg := parseExchangeSecretConfig(sec.ExtraConfig)
		marketTypes, autoDetected, err := s.marketTypesForSecret(ctx, provider, sec, cfg, opts)
		if err != nil {
			return nil, err
		}
		for _, marketType := range marketTypes {
			account, err := s.upsertExchangeAccount(ctx, spaceID, opts, provider, sec, cfg, marketType, autoDetected)
			if err != nil {
				return nil, err
			}
			out = append(out, account)
		}
	}
	return out, nil
}

func (s *AccountService) upsertExchangeAccount(ctx context.Context, spaceID string, opts SyncExchangeAccountsOptions,
	provider string, sec ExchangeSecret, cfg exchangeSecretConfig, marketType string, autoDetected bool) (*Account, error) {
	idSource := sec.SecretID
	if autoDetected {
		idSource = sec.SecretID + "|" + marketType
	}
	accountID := deterministicID("acc", idSource)
	apiKeyID := deterministicID("ak", idSource)
	channelID := deterministicID("ch", idSource)
	if autoDetected {
		apiKeyID = deterministicID("ak", accountID)
		channelID = deterministicID("ch", accountID)
	}
	accountName := secretAccountName(sec)
	if autoDetected {
		accountName += "-" + marketType
	}

	account := &Account{
		AccountID:    accountID,
		UserID:       opts.UserID,
		AccountName:  accountName,
		AccountType:  accountTypeForMarket(marketType),
		ChannelID:    channelID,
		BaseCurrency: defaultString(cfg.BaseCurrency, "USDT"),
		Status:       AccountNormal,
		Remark:       defaultString(sec.Description, "imported from admin secret "+sec.SecretID),
	}
	if existing, err := s.store.GetAccount(ctx, spaceID, accountID); err == nil && existing != nil {
		account.IsDefault = existing.IsDefault
		if err := s.store.UpdateAccount(ctx, spaceID, account); err != nil {
			return nil, err
		}
	} else if err == ErrNotFound {
		if err := s.store.CreateAccount(ctx, spaceID, account); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	if _, err := s.store.GetAPIKey(ctx, spaceID, apiKeyID); err == ErrNotFound {
		perms := cfg.Permissions
		if len(perms) == 0 {
			perms = []string{"read"}
		}
		if err := s.store.CreateAPIKey(ctx, spaceID, &APIKey{
			APIKeyID:       apiKeyID,
			AccountID:      accountID,
			Exchange:       provider,
			APIKey:         sec.KeyID,
			APISecret:      sec.SecretValue,
			Passphrase:     cfg.Passphrase,
			PermissionsRaw: perms,
			Status:         1,
		}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	channel := &TradeChannel{
		ChannelID:   channelID,
		ChannelName: account.AccountName + "-" + marketType,
		Exchange:    provider,
		MarketType:  marketType,
		AccountID:   accountID,
		APIKeyID:    apiKeyID,
		Endpoint:    cfg.Endpoint,
		Status:      1,
		RateLimit:   cfg.RateLimit,
	}
	if _, err := s.store.GetChannel(ctx, spaceID, channelID); err == nil {
		if err := s.store.UpdateChannel(ctx, spaceID, channel); err != nil {
			return nil, err
		}
	} else if err == ErrNotFound {
		if err := s.store.CreateChannel(ctx, spaceID, channel); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	return account, nil
}

func (s *AccountService) adapterForChannel(ctx context.Context, spaceID string, ch *TradeChannel) (exchange.ExchangeAdapter, exchange.Credential, error) {
	var cred exchange.Credential
	if ch == nil || s.exNew == nil {
		return nil, cred, ErrInvalidParam
	}
	adapter, err := s.exNew(ch.Exchange)
	if err != nil {
		return nil, cred, err
	}
	if ch.APIKeyID != "" {
		k, err := s.store.GetAPIKey(ctx, spaceID, ch.APIKeyID)
		if err != nil {
			return nil, cred, err
		}
		cred = exchange.Credential{APIKey: k.APIKey, APISecret: k.APISecret, Passphrase: k.Passphrase}
	}
	return adapter, cred, nil
}

func (s *AccountService) marketTypesForSecret(ctx context.Context, provider string, sec ExchangeSecret,
	cfg exchangeSecretConfig, opts SyncExchangeAccountsOptions) ([]string, bool, error) {
	if marketType := requestedMarketType(cfg, opts); marketType != "" {
		return []string{marketType}, false, nil
	}
	if s.exNew == nil {
		return []string{"spot"}, true, nil
	}

	adapter, err := s.exNew(provider)
	if err != nil {
		return nil, true, err
	}
	cred := exchange.Credential{
		APIKey:     sec.KeyID,
		APISecret:  sec.SecretValue,
		Passphrase: cfg.Passphrase,
	}
	marketTypes := make([]string, 0, 2)
	for _, candidate := range []string{"spot", "swap"} {
		if _, err := adapter.GetBalances(ctx, cred, exchangeMarketType(candidate), nil); err == nil {
			marketTypes = append(marketTypes, candidate)
		} else {
			log.DebugContextf(ctx, "trade: skip exchange secret market provider=%s secret_id=%s market_type=%s err=%v",
				provider, sec.SecretID, candidate, err)
		}
	}
	return marketTypes, true, nil
}

func requestedMarketType(cfg exchangeSecretConfig, opts SyncExchangeAccountsOptions) string {
	if marketType := strings.TrimSpace(cfg.MarketType); marketType != "" {
		return marketType
	}
	return strings.TrimSpace(opts.MarketType)
}

func exchangeMarketType(marketType string) exchange.MarketType {
	switch normalizeMarketType(marketType) {
	case "swap", "futures":
		return exchange.MarketSwap
	default:
		return exchange.MarketSpot
	}
}

// genID 生成带前缀的随机 ID（小写字母+数字，11 位）。
func genID(prefix string) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	id, err := gonanoid.Generate(alphabet, 11)
	if err != nil {
		id = gonanoid.MustGenerate(alphabet, 11)
	}
	return prefix + "_" + id
}
