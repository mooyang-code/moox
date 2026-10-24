package dao

import (
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/softdelete"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
)

// 系统秘钥（网关密钥环、主机 SSH 凭据）由所属服务直接读写，不经过通用的 SecretDAO。
// 下面的函数接收 *gorm.DB，调用方可以传事务句柄，让秘钥与它所属的业务记录一起提交。

// PutSystemSecret 用 encryptionKey 加密后写入系统秘钥，已有同 ID 的记录被整体替换。
func PutSystemSecret(db *gorm.DB, encryptionKey string, secret *model.Secret) error {
	if secret == nil || !secret.IsSystem() || secret.SecretID == "" {
		return errors.New("只能写入系统秘钥")
	}
	encrypted, err := mooxsecurity.Encrypt(secret.SecretValue, encryptionKey)
	if err != nil {
		return err
	}
	record := *secret
	record.SecretValue = encrypted
	record.ID = 0
	record.IsDeleted = false
	now := time.Now()
	record.CreateTime, record.ModifyTime = now, now
	if err := db.Where("c_secret_id = ?", secret.SecretID).Delete(&model.Secret{}).Error; err != nil {
		return err
	}
	return db.Create(&record).Error
}

// GetSystemSecret 读取并解密系统秘钥；不存在时返回 gorm.ErrRecordNotFound。
func GetSystemSecret(db *gorm.DB, encryptionKey, secretID string) (*model.Secret, error) {
	var secret model.Secret
	if err := db.Where("c_secret_id = ? AND c_is_deleted = ?", secretID, softdelete.IsDeletedFalse).First(&secret).Error; err != nil {
		return nil, err
	}
	if !secret.IsSystem() {
		return nil, ErrSecretNotFound
	}
	plain, err := mooxsecurity.Decrypt(secret.SecretValue, encryptionKey)
	if err != nil {
		return nil, err
	}
	secret.SecretValue = plain
	return &secret, nil
}

// DeleteSystemSecret 永久删除系统秘钥；记录不存在不算错误。
func DeleteSystemSecret(db *gorm.DB, secretID string) error {
	if !(&model.Secret{SecretID: secretID}).IsSystem() {
		return errors.New("只能删除系统秘钥")
	}
	return db.Where("c_secret_id = ?", secretID).Delete(&model.Secret{}).Error
}
