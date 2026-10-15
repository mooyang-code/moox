package setup

import (
	"context"
	"errors"
	"strings"
	"time"

	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
)

const (
	// configBackupID 是配置备份在 t_secrets 里的固定标识；整个系统只有一份。
	configBackupID = "moox-config-backup"
	// configBackupMagic 是 CLI 生成的备份信封的开头。服务端只认格式，不解密（也解不开）。
	configBackupMagic = "MOOX-CONFIG-BACKUP v1\n"
	maxConfigBackup   = 512 << 10
)

var (
	ErrBackupInvalid = errors.New("config_backup_invalid")
	ErrBackupMissing = errors.New("config_backup_missing")
)

// Backup 是保存在管理后台的配置备份。
type Backup struct {
	Envelope  string
	UpdatedAt time.Time
}

// PutBackup 保存（覆盖）配置备份。信封入库前再用管理后台主密钥加密。
func (s *Service) PutBackup(ctx context.Context, envelope string) (time.Time, error) {
	if !strings.HasPrefix(envelope, configBackupMagic) || len(envelope) > maxConfigBackup {
		return time.Time{}, ErrBackupInvalid
	}
	encrypted, err := mooxsecurity.Encrypt(envelope, s.encryptionKey)
	if err != nil {
		return time.Time{}, ErrStorage
	}
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing secretmodel.Secret
		err := tx.Where("c_secret_id = ? AND c_is_deleted = 0", configBackupID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&secretmodel.Secret{
				SecretID: configBackupID, Name: "MooX 配置备份", Description: "用管理员密码加密的 moox.toml，换电脑时用 moox-cli config restore 恢复",
				Category: "other", Provider: "moox", SecretType: "other", SecretValue: encrypted,
				ExtraConfig: "{}", Status: "active", Creator: "moox-cli", CreateTime: now, ModifyTime: now,
			}).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&secretmodel.Secret{}).Where("c_id = ?", existing.ID).
			Updates(map[string]any{"c_secret_value": encrypted, "c_status": "active", "c_mtime": now}).Error
	})
	if err != nil {
		return time.Time{}, ErrStorage
	}
	if err := s.afterWrite("secret"); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// GetBackup 取回配置备份（仍是 CLI 加密的信封）。
func (s *Service) GetBackup(ctx context.Context) (Backup, error) {
	var secret secretmodel.Secret
	err := s.db.WithContext(ctx).Where("c_secret_id = ? AND c_is_deleted = 0 AND c_status = 'active'", configBackupID).First(&secret).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Backup{}, ErrBackupMissing
	}
	if err != nil {
		return Backup{}, ErrStorage
	}
	envelope, err := mooxsecurity.Decrypt(secret.SecretValue, s.encryptionKey)
	if err != nil || !strings.HasPrefix(envelope, configBackupMagic) {
		return Backup{}, ErrStorage
	}
	return Backup{Envelope: envelope, UpdatedAt: secret.ModifyTime}, nil
}
