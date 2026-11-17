package cloud

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// ErrAccountInUse 表示云账号还在被使用，不能删除。
var ErrAccountInUse = errors.New("云账号还在使用中")

// Accounts 管理云账号。账号只登记密钥 ID 与 COS 桶，密钥本身在 Admin 的 SecretMgr。
type Accounts struct {
	accounts *store.CloudAccountRepository
	packages *store.FunctionPackageRepository
}

// NewAccounts 用 Collector 库里的云账号与代码包表构造云账号管理。
func NewAccounts(db *store.Store) *Accounts {
	return &Accounts{accounts: db.CloudAccounts(), packages: db.FunctionPackages()}
}

// List 返回全部云账号。
func (a *Accounts) List(ctx context.Context) ([]store.CloudAccount, error) {
	return a.accounts.List(ctx)
}

// Get 返回一个云账号；不存在时返回 nil。
func (a *Accounts) Get(ctx context.Context, accountID string) (*store.CloudAccount, error) {
	return a.accounts.Get(ctx, accountID)
}

// Save 新建或整行更新一个云账号。只支持腾讯云，必须登记 SecretMgr 里的密钥 ID。
func (a *Accounts) Save(ctx context.Context, account store.CloudAccount) error {
	account.AccountID = strings.TrimSpace(account.AccountID)
	account.CredentialSecretID = strings.TrimSpace(account.CredentialSecretID)
	if account.Provider = strings.TrimSpace(account.Provider); account.Provider == "" {
		account.Provider = "tencent"
	}
	if account.AccountID == "" {
		return fmt.Errorf("云账号 ID 不能为空")
	}
	if account.Provider != "tencent" {
		return fmt.Errorf("只支持腾讯云账号，收到的厂商是 %q", account.Provider)
	}
	if account.CredentialSecretID == "" {
		return fmt.Errorf("云账号必须登记密钥 ID（SecretMgr 里类别为 cloud、厂商为 tencent 的秘钥）")
	}
	return a.accounts.Upsert(ctx, account)
}

// Delete 删除一个云账号。账号下还有代码包时拒绝（返回 ErrAccountInUse）；是否还被 scf_plan 配置引用由调用方判断。
func (a *Accounts) Delete(ctx context.Context, accountID string) error {
	count, err := a.packages.CountByAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w：账号 %s 下还有 %d 个代码包，先删掉代码包", ErrAccountInUse, accountID, count)
	}
	return a.accounts.Delete(ctx, accountID)
}
