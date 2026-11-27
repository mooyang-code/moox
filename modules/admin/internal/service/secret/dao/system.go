package dao

import (
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"gorm.io/gorm"
)

// 系统秘钥（主机 SSH 凭据）由所属服务直接读写，不经过通用的 SecretDAO。
// 下面的函数接收 *gorm.DB，调用方可以传事务句柄，让秘钥与它所属的业务记录一起提交。

// PutSystemSecret 用 encryptionKey 加密后写入系统秘钥，已有同 ID 的记录被整体替换。
func PutSystemSecret(db *gorm.DB, encryptionKey string, secret *model.Secret) error {
	if secret == nil || !secret.IsSystem() || secret.SecretID == "" {
		return errors.New("只能写入系统秘钥")
	}
	record := *secret
	if err := EncryptSecretFields(&record, encryptionKey); err != nil {
		return err
	}
	record.ID = 0
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
	if err := db.Where("c_secret_id = ?", secretID).First(&secret).Error; err != nil {
		return nil, err
	}
	if !secret.IsSystem() {
		return nil, ErrSecretNotFound
	}
	if err := DecryptSecretFields(&secret, encryptionKey); err != nil {
		return nil, err
	}
	return &secret, nil
}

// DeleteSystemSecret 永久删除系统秘钥；记录不存在不算错误。
func DeleteSystemSecret(db *gorm.DB, secretID string) error {
	if !(&model.Secret{SecretID: secretID}).IsSystem() {
		return errors.New("只能删除系统秘钥")
	}
	return db.Where("c_secret_id = ?", secretID).Delete(&model.Secret{}).Error
}
