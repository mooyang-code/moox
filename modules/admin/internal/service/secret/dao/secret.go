package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"trpc.group/trpc-go/trpc-go/log"
)

// ErrSecretNotFound 秘钥不存在或已删除。
var ErrSecretNotFound = errors.New("secret not found or already deleted")

// ErrMaskedValue 秘钥值包含脱敏字符，拒绝加密。
var ErrMaskedValue = errors.New("secret_value is a masked value, refusing to encrypt")

// ErrSystemSecret 系统秘钥不允许通过通用 SecretDAO 读写。
var ErrSystemSecret = errors.New("system secret is managed by its owning service")

// ErrMissingEncryptionKey 未提供主密钥。
var ErrMissingEncryptionKey = errors.New("admin encryption key is not configured")

// EncryptSecretFields 是 t_secrets 全部写入口统一的 AES-GCM 加密函数：
// 同时加密 c_secret_value 与 c_extra_config，拒绝脱敏占位串落库。
func EncryptSecretFields(secret *model.Secret, encryptionKey string) error {
	if secret == nil {
		return errors.New("secret is nil")
	}
	if strings.TrimSpace(encryptionKey) == "" {
		return ErrMissingEncryptionKey
	}
	if secret.SecretValue != "" {
		if strings.Contains(secret.SecretValue, "•") {
			return ErrMaskedValue
		}
		encrypted, err := mooxsecurity.Encrypt(secret.SecretValue, encryptionKey)
		if err != nil {
			return err
		}
		secret.SecretValue = encrypted
	}
	extra := secret.ExtraConfig
	if strings.TrimSpace(extra) == "" {
		extra = "{}"
	}
	encryptedExtra, err := mooxsecurity.Encrypt(extra, encryptionKey)
	if err != nil {
		return err
	}
	secret.ExtraConfig = encryptedExtra
	return nil
}

// DecryptSecretFields 是 t_secrets 全部读入口统一的 AES-GCM 解密函数：
// 同时解密 c_secret_value 与 c_extra_config。
func DecryptSecretFields(secret *model.Secret, encryptionKey string) error {
	if secret == nil {
		return errors.New("secret is nil")
	}
	if strings.TrimSpace(encryptionKey) == "" {
		return ErrMissingEncryptionKey
	}
	if secret.SecretValue != "" {
		decrypted, err := mooxsecurity.Decrypt(secret.SecretValue, encryptionKey)
		if err != nil {
			return err
		}
		secret.SecretValue = decrypted
	}
	if secret.ExtraConfig != "" {
		decryptedExtra, err := mooxsecurity.Decrypt(secret.ExtraConfig, encryptionKey)
		if err != nil {
			return err
		}
		secret.ExtraConfig = decryptedExtra
	}
	return nil
}

// IsDeletedInitialSecret 报告某秘钥 ID 是否曾在管理台被主动硬删除。
func IsDeletedInitialSecret(ctx context.Context, db *gorm.DB, secretID string) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).Model(&model.DeletedInitialSecret{}).Where("c_secret_id = ?", secretID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// SecretDAO 秘钥数据访问层（物理硬删除，显式传入主密钥）。
type SecretDAO struct {
	db            *gorm.DB
	encryptionKey string
}

// NewSecretDAO 创建 DAO 实例；encryptionKey 由启动期或离线工具显式传入。
func NewSecretDAO(db *gorm.DB, encryptionKey string) *SecretDAO {
	return &SecretDAO{db: db, encryptionKey: strings.TrimSpace(encryptionKey)}
}

// Create 创建秘钥（拷贝后再加密，不原地污染调用方入参）；若同初始 ID 存在墓碑则一并清除。
func (d *SecretDAO) Create(ctx context.Context, secret *model.Secret) error {
	if secret == nil {
		return errors.New("secret is nil")
	}
	if isSystemSecret(secret) {
		return ErrSystemSecret
	}
	record := *secret
	if err := EncryptSecretFields(&record, d.encryptionKey); err != nil {
		return err
	}
	now := time.Now()
	record.CreateTime = now
	record.ModifyTime = now
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if model.IsInitialSecretID(record.SecretID) {
			return tx.Where("c_secret_id = ?", record.SecretID).Delete(&model.DeletedInitialSecret{}).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	secret.ID = record.ID
	secret.CreateTime = record.CreateTime
	secret.ModifyTime = record.ModifyTime
	return nil
}

// Update 更新秘钥（拷贝后再加密，不原地污染调用方入参）。
func (d *SecretDAO) Update(ctx context.Context, secret *model.Secret) error {
	if secret == nil {
		return errors.New("secret is nil")
	}
	if isSystemSecret(secret) {
		return ErrSystemSecret
	}
	record := *secret
	if err := EncryptSecretFields(&record, d.encryptionKey); err != nil {
		return err
	}
	record.ModifyTime = time.Now()
	result := d.db.WithContext(ctx).Scopes(userSecrets).Model(&record).Where("c_secret_id = ?", record.SecretID).
		Select("*").Omit("c_id", "c_ctime", "c_secret_id").Updates(&record)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSecretNotFound
	}
	secret.ModifyTime = record.ModifyTime
	return nil
}

// Delete 物理硬删除秘钥；仅对初始交易所密钥（exchange-*）写墓碑，防止 setup init 静默重建。
func (d *SecretDAO) Delete(ctx context.Context, secretID string) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Scopes(userSecrets).Where("c_secret_id = ?", secretID).Delete(&model.Secret{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrSecretNotFound
		}
		if !model.IsInitialSecretID(secretID) {
			return nil
		}
		tombstone := model.DeletedInitialSecret{
			SecretID:  secretID,
			CreatedAt: time.Now().UTC().UnixMilli(),
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "c_secret_id"}},
			UpdateAll: true,
		}).Create(&tombstone).Error
	})
}

// FindByID 根据唯一标识查询。
func (d *SecretDAO) FindByID(ctx context.Context, secretID string) (*model.Secret, error) {
	var secret model.Secret
	err := d.db.WithContext(ctx).Scopes(userSecrets).Where("c_secret_id = ?", secretID).First(&secret).Error
	if err != nil {
		return nil, err
	}
	if err := DecryptSecretFields(&secret, d.encryptionKey); err != nil {
		return nil, err
	}
	return &secret, nil
}

// List 分页查询秘钥列表。
func (d *SecretDAO) List(ctx context.Context, offset, limit int, filters *SecretFilters) ([]model.Secret, int64, error) {
	var secrets []model.Secret
	var total int64

	db := d.db.WithContext(ctx).Scopes(userSecrets).Model(&model.Secret{})
	db = d.applyFilters(db, filters)

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Order("c_mtime desc").Offset(offset).Limit(limit).Find(&secrets).Error; err != nil {
		return nil, 0, err
	}

	for i := range secrets {
		if err := DecryptSecretFields(&secrets[i], d.encryptionKey); err != nil {
			log.ErrorContextf(ctx, "[Secret DAO] 解密秘钥 %s 的敏感字段失败: %v", secrets[i].SecretID, err)
			return nil, 0, fmt.Errorf("解密秘钥 %s 失败: %w", secrets[i].SecretID, err)
		}
	}
	return secrets, total, nil
}

// SecretFilters 列表查询过滤条件。
type SecretFilters struct {
	Keyword  string
	Category string
	Provider string
	Status   string
}

func (d *SecretDAO) applyFilters(db *gorm.DB, f *SecretFilters) *gorm.DB {
	if f == nil {
		return db
	}
	if f.Keyword != "" {
		db = db.Where("c_name LIKE ? OR c_description LIKE ?", "%"+f.Keyword+"%", "%"+f.Keyword+"%")
	}
	if f.Category != "" {
		db = db.Where("c_category = ?", f.Category)
	}
	if f.Provider != "" {
		db = db.Where("c_provider = ?", f.Provider)
	}
	if f.Status != "" {
		db = db.Where("c_status = ?", f.Status)
	}
	return db
}

// UpdateStatus 更新秘钥状态。
func (d *SecretDAO) UpdateStatus(ctx context.Context, secretID, status string) error {
	result := d.db.WithContext(ctx).Scopes(userSecrets).Model(&model.Secret{}).
		Where("c_secret_id = ?", secretID).
		Update("c_status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// GenerateSecretID 生成秘钥唯一标识。
func GenerateSecretID() string {
	return uuid.New().String()
}

func isSystemSecret(secret *model.Secret) bool {
	return secret != nil && secret.IsSystem()
}

func userSecrets(db *gorm.DB) *gorm.DB {
	return db.Where("substr(c_secret_id, 1, ?) <> ?", len(model.SSHHostIDPrefix), model.SSHHostIDPrefix)
}
