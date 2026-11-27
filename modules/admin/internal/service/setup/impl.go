package setup

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
)

func (s *Service) Apply(ctx context.Context, manifest Manifest) (Result, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	result := expectedResult(manifest)
	if err := validateManifest(manifest, s.db, s.encryptionKey); err != nil {
		return Result{}, err
	}
	keys, err := normalizeExchangeKeys(manifest.ExchangeKeys)
	if err != nil {
		return Result{}, err
	}
	created := 0
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 管理员由部署时的 moox-admin-cli user ensure 建，这里只核对：密码与秘密文件不一致（在管理台改过）记进 Kept。
		// 腾讯云密钥与 moox.toml 不一致时同样以管理台为准：不改、不报错，记进 Kept。
		result.Kept = result.Kept[:0]
		adminState, err := s.inspectAdmin(ctx, tx, manifest.Admin)
		if err != nil {
			return err
		}
		switch adminState {
		case recordMissing:
			return ErrAdminMissing
		case recordKept:
			result.Kept = append(result.Kept, keptAdmin)
		}
		n, tencentState, err := s.ensureTencentSecret(ctx, tx, manifest.TencentCloud)
		if err != nil {
			return err
		}
		created += n
		if tencentState == recordKept {
			result.Kept = append(result.Kept, keptTencentCloud)
		}
		// 交易所密钥只建不改，不进整体冲突判断：已存在或已在管理台删除的都原样保留。
		result.ExchangeKeys = result.ExchangeKeys[:0]
		for _, key := range keys {
			keyResult, n, err := s.ensureExchangeKey(ctx, tx, key)
			if err != nil {
				return err
			}
			created += n
			result.ExchangeKeys = append(result.ExchangeKeys, keyResult)
		}
		result.Secrets = secretsPresent(tencentState != recordDeleted, len(keys), result.ExchangeKeys)
		return nil
	})
	if err != nil {
		normalized := normalizeError(err)
		if errors.Is(normalized, ErrStorage) {
			status, inspectErr := s.Inspect(ctx, manifest)
			if inspectErr == nil && status.State == "completed" {
				result.Action = "unchanged"
				result.Secrets = status.Secrets
				result.ExchangeKeys = status.ExchangeKeys
				result.Kept = status.Kept
				return result, nil
			}
		}
		return Result{}, normalized
	}
	result.Action = "unchanged"
	if created > 0 {
		result.Action = "created"
	}
	return result, nil
}

func (s *Service) Inspect(ctx context.Context, manifest Manifest) (Status, error) {
	if err := validateManifest(manifest, s.db, s.encryptionKey); err != nil {
		return Status{}, err
	}
	keys, err := normalizeExchangeKeys(manifest.ExchangeKeys)
	if err != nil {
		return Status{}, err
	}
	status := Status{State: "completed", Users: 1, Secrets: 1 + len(keys)}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := s.inspectAdmin(ctx, tx, manifest.Admin)
		if err != nil {
			return err
		}
		status.add(state, keptAdmin)
		tencentState, err := s.inspectTencentSecret(ctx, tx, manifest.TencentCloud)
		if err != nil {
			return err
		}
		status.add(tencentState, keptTencentCloud)
		for _, key := range keys {
			keyState, err := s.inspectExchangeKey(ctx, tx, key)
			if err != nil {
				return err
			}
			// 只有真正缺失的密钥算 Missing；已存在、已在管理台删除都算完成，否则 setup init 会永远报未完成。
			if keyState == ExchangeKeyMissing {
				status.add(recordMissing, "")
			}
			status.ExchangeKeys = append(status.ExchangeKeys, ExchangeKeyResult{SecretID: exchangeKeyID(key), State: keyState, ReadOnly: key.ReadOnly})
		}
		status.Secrets = secretsPresent(tencentState != recordDeleted, len(keys), status.ExchangeKeys)
		return nil
	})
	if err != nil {
		return Status{}, normalizeError(err)
	}
	if status.Missing > 0 {
		status.State = "incomplete"
	}
	return status, nil
}

// secretsPresent 是应当存在的密钥数：未在管理台删除的腾讯云密钥加每把未删除的交易所密钥
// （已在管理台删除的初始密钥不会由 setup 重建，计入会让数量永远对不上）。
func secretsPresent(tencentPresent bool, keyCount int, results []ExchangeKeyResult) int {
	present := keyCount
	if tencentPresent {
		present++
	}
	for _, result := range results {
		if result.State == ExchangeKeyDeleted {
			present--
		}
	}
	return present
}

// expectedResult 的 Secrets 是 moox.toml 要求存在的密钥数：腾讯云一把加每把交易所密钥。
func expectedResult(manifest Manifest) Result {
	return Result{Users: 1, Secrets: 1 + len(manifest.ExchangeKeys)}
}

func validateManifest(manifest Manifest, db *gorm.DB, encryptionKey string) error {
	if db == nil || encryptionKey == "" || strings.TrimSpace(manifest.Admin.Username) == "" || manifest.Admin.Password == "" ||
		strings.TrimSpace(manifest.TencentCloud.SecretID) == "" || manifest.TencentCloud.SecretKey == "" {
		return ErrInvalid
	}
	if _, err := normalizeExchangeKeys(manifest.ExchangeKeys); err != nil {
		return err
	}
	return nil
}

type recordState uint8

const (
	recordMatch recordState = iota
	recordMissing
	// recordKept 是库里已有、但与 moox.toml 不一致：以管理台为准保留，不改写。
	recordKept
	// recordDeleted 是已在管理台硬删除并记录在墓碑表的初始密钥：setup 不再静默复活。
	recordDeleted
)

// Kept 里的名称：管理员、腾讯云密钥。
const (
	keptAdmin        = "admin"
	keptTencentCloud = "tencent_cloud"
)

func (s *Status) add(state recordState, name string) {
	switch state {
	case recordMissing:
		s.Missing++
	case recordKept:
		s.Kept = append(s.Kept, name)
	}
}

func (s *Service) inspectAdmin(ctx context.Context, tx *gorm.DB, input Admin) (recordState, error) {
	var user auth.User
	err := tx.WithContext(ctx).Where("c_username = ?", strings.TrimSpace(input.Username)).First(&user).Error
	if err == nil {
		if mooxsecurity.VerifyPassword(input.Password, user.PasswordHash) {
			return recordMatch, nil
		}
		return recordKept, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return recordKept, ErrStorage
	}
	// 系统只有一个超级用户：库里已有别的用户就是冲突。
	var otherUsers int64
	if err := tx.WithContext(ctx).Model(&auth.User{}).Count(&otherUsers).Error; err != nil {
		return recordKept, ErrStorage
	}
	if otherUsers > 0 {
		return recordKept, nil
	}
	return recordMissing, nil
}

func (s *Service) ensureTencentSecret(ctx context.Context, tx *gorm.DB, input TencentCloud) (int, recordState, error) {
	state, err := s.inspectTencentSecret(ctx, tx, input)
	if err != nil || state == recordMatch || state == recordKept || state == recordDeleted {
		return 0, state, err
	}
	now := time.Now()
	secret := &secretmodel.Secret{
		SecretID: tencentSecretID, Name: "Tencent Cloud Default", Category: "cloud", Provider: "tencent",
		SecretType: "api_key", KeyID: strings.TrimSpace(input.SecretID), SecretValue: input.SecretKey,
		ExtraConfig: "{}", Status: "active", Creator: "setup", CreateTime: now, ModifyTime: now,
	}
	if err := secretdao.EncryptSecretFields(secret, s.encryptionKey); err != nil {
		return 0, recordKept, ErrStorage
	}
	if err := tx.WithContext(ctx).Create(secret).Error; err != nil {
		return 0, recordKept, ErrStorage
	}
	if err := tx.WithContext(ctx).Where("c_secret_id = ?", tencentSecretID).Delete(&secretmodel.DeletedInitialSecret{}).Error; err != nil {
		return 0, recordKept, ErrStorage
	}
	if err := s.afterWrite("secret"); err != nil {
		return 0, recordKept, err
	}
	return 1, recordMatch, nil
}

func (s *Service) inspectTencentSecret(ctx context.Context, tx *gorm.DB, input TencentCloud) (recordState, error) {
	var secret secretmodel.Secret
	err := tx.WithContext(ctx).Where("c_secret_id = ?", tencentSecretID).First(&secret).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		deleted, tombstoneErr := secretdao.IsDeletedInitialSecret(ctx, tx, tencentSecretID)
		if tombstoneErr != nil {
			return recordKept, ErrStorage
		}
		if deleted {
			return recordDeleted, nil
		}
		return recordMissing, nil
	}
	if err != nil {
		return recordKept, ErrStorage
	}
	if err := secretdao.DecryptSecretFields(&secret, s.encryptionKey); err != nil {
		return recordKept, ErrStorage
	}
	metadataMatches := secret.Name == "Tencent Cloud Default" && secret.Category == "cloud" && secret.Provider == "tencent" &&
		secret.SecretType == "api_key" && secret.KeyID == strings.TrimSpace(input.SecretID) && secret.Status == "active"
	if metadataMatches && secretEqual(secret.SecretValue, input.SecretKey) {
		return recordMatch, nil
	}
	return recordKept, nil
}

func secretEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (s *Service) afterWrite(stage string) error {
	if s.writeHook == nil {
		return nil
	}
	if err := s.writeHook(stage); err != nil {
		return ErrStorage
	}
	return nil
}

func normalizeError(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return ErrInvalid
	case errors.Is(err, ErrAdminMissing):
		return ErrAdminMissing
	default:
		return ErrStorage
	}
}
