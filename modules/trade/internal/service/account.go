package service

import (
	"context"
	"strings"

	gonanoid "github.com/matoous/go-nanoid/v2"
)

// AccountService 实现账户域：账户、余额、资金流水、API 凭证。
type AccountService struct {
	store Store
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

// genID 生成带前缀的随机 ID（小写字母+数字，11 位）。
func genID(prefix string) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	id, err := gonanoid.Generate(alphabet, 11)
	if err != nil {
		id = gonanoid.MustGenerate(alphabet, 11)
	}
	return prefix + "_" + id
}
