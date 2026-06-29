package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/admin/internal/common"
	"github.com/mooyang-code/moox/modules/admin/internal/common/crypto"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// ErrSecretNotFound 秘钥不存在或已删除
var ErrSecretNotFound = errors.New("secret not found or already deleted")

// ErrMaskedValue 秘钥值包含脱敏字符，拒绝加密
var ErrMaskedValue = errors.New("secret_value is a masked value, refusing to encrypt")

// SecretDAO 秘钥数据访问层
type SecretDAO struct {
	db *gorm.DB
}

// NewSecretDAO 创建 DAO 实例
func NewSecretDAO(db *gorm.DB) *SecretDAO {
	return &SecretDAO{db: db}
}

// Create 创建秘钥
func (d *SecretDAO) Create(ctx context.Context, secret *model.Secret) error {
	if err := d.encryptSensitiveFields(secret); err != nil {
		return err
	}
	secret.CreateTime = time.Now()
	secret.ModifyTime = time.Now()
	return d.db.WithContext(ctx).Create(secret).Error
}

// Update 更新秘钥
func (d *SecretDAO) Update(ctx context.Context, secret *model.Secret) error {
	if err := d.encryptSensitiveFields(secret); err != nil {
		return err
	}
	secret.ModifyTime = time.Now()
	result := d.db.WithContext(ctx).Model(secret).Where("c_secret_id = ?", secret.SecretID).
		Select("*").Omit("c_id", "c_ctime", "c_secret_id", "c_space_id").Updates(secret)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// Delete 软删除秘钥
func (d *SecretDAO) Delete(ctx context.Context, secretID string) error {
	result := d.db.WithContext(ctx).Model(&model.Secret{}).
		Where("c_secret_id = ? AND c_is_deleted = ?", secretID, common.IsDeletedFalse).
		Update("c_is_deleted", common.IsDeletedTrue)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// FindByID 根据唯一标识查询
func (d *SecretDAO) FindByID(ctx context.Context, secretID string) (*model.Secret, error) {
	var secret model.Secret
	err := d.db.WithContext(ctx).Where("c_secret_id = ? AND c_is_deleted = ?", secretID, common.IsDeletedFalse).First(&secret).Error
	if err != nil {
		return nil, err
	}
	if err := d.decryptSensitiveFields(&secret); err != nil {
		return nil, err
	}
	return &secret, nil
}

// List 分页查询秘钥列表
func (d *SecretDAO) List(ctx context.Context, offset, limit int, filters *SecretFilters) ([]model.Secret, int64, error) {
	var secrets []model.Secret
	var total int64

	db := d.db.WithContext(ctx).Model(&model.Secret{}).Where("c_is_deleted = ?", common.IsDeletedFalse)
	db = d.applyFilters(db, filters)

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Order("c_mtime desc").Offset(offset).Limit(limit).Find(&secrets).Error; err != nil {
		return nil, 0, err
	}

	for i := range secrets {
		if err := d.decryptSensitiveFields(&secrets[i]); err != nil {
			log.ErrorContextf(ctx, "[Secret DAO] 解密秘钥 %s 的敏感字段失败: %v", secrets[i].SecretID, err)
			return nil, 0, fmt.Errorf("解密秘钥 %s 失败: %w", secrets[i].SecretID, err)
		}
	}
	return secrets, total, nil
}

// SecretFilters 列表查询过滤条件
type SecretFilters struct {
	Keyword  string
	Category string
	Provider string
	Status   string
}

// applyFilters 应用查询过滤条件
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

// UpdateStatus 更新秘钥状态
func (d *SecretDAO) UpdateStatus(ctx context.Context, secretID, status string) error {
	result := d.db.WithContext(ctx).Model(&model.Secret{}).
		Where("c_secret_id = ? AND c_is_deleted = ?", secretID, common.IsDeletedFalse).
		Update("c_status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// UpdateLastUsed 更新最后使用信息
func (d *SecretDAO) UpdateLastUsed(ctx context.Context, secretID, usedBy string) error {
	result := d.db.WithContext(ctx).Model(&model.Secret{}).
		Where("c_secret_id = ? AND c_is_deleted = ?", secretID, common.IsDeletedFalse).
		Updates(map[string]interface{}{
			"c_last_used_at": time.Now(),
			"c_last_used_by": usedBy,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// encryptSensitiveFields 加密敏感字段。
// 防御纵深：拒绝含 • (脱敏字符) 的串，防止脱敏串被当明文加密入库。
// 真实秘钥不会包含 •，但可能包含 *，因此用 • 做脱敏标记。
func (d *SecretDAO) encryptSensitiveFields(secret *model.Secret) error {
	if secret.SecretValue == "" {
		return nil
	}
	if strings.Contains(secret.SecretValue, "•") {
		return ErrMaskedValue
	}
	key := crypto.GetEncryptionKey()
	encrypted, err := crypto.AESEncrypt(secret.SecretValue, key)
	if err != nil {
		return err
	}
	secret.SecretValue = encrypted
	return nil
}

// decryptSensitiveFields 解密敏感字段
func (d *SecretDAO) decryptSensitiveFields(secret *model.Secret) error {
	key := crypto.GetEncryptionKey()
	if secret.SecretValue != "" {
		decrypted, err := crypto.AESDecrypt(secret.SecretValue, key)
		if err != nil {
			return err
		}
		secret.SecretValue = decrypted
	}
	return nil
}

// GenerateSecretID 生成秘钥唯一标识
func GenerateSecretID() string {
	return uuid.New().String()
}
