package dao

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mooyang-code/moox/modules/trade/internal/common/crypto"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
)

// errNoEncryptionKey 缺少加密密钥。
var errNoEncryptionKey = errors.New("encryption key not configured for trade DAO")

// CreateAPIKey 加密落库 API 凭证。permissions []string 序列化为 JSON 存 c_permissions。
func (g *GormStore) CreateAPIKey(ctx context.Context, spaceID string, k *service.APIKey) error {
	if g.encryptionKey == "" {
		return errNoEncryptionKey
	}
	encSecret, err := crypto.AESEncrypt(k.APISecret, g.encryptionKey)
	if err != nil {
		return err
	}
	encPass := ""
	if k.Passphrase != "" {
		encPass, err = crypto.AESEncrypt(k.Passphrase, g.encryptionKey)
		if err != nil {
			return err
		}
	}
	permJSON, err := json.Marshal(k.PermissionsRaw)
	if err != nil {
		return err
	}
	k.SpaceID = spaceID
	if k.IsDeleted == "" {
		k.IsDeleted = service.IsDeletedFalse
	}
	row := &service.APIKey{
		SpaceID:     spaceID,
		APIKeyID:    k.APIKeyID,
		AccountID:   k.AccountID,
		Exchange:    k.Exchange,
		APIKey:      k.APIKey,
		APISecret:   encSecret,
		Passphrase:  encPass,
		Permissions: string(permJSON),
		Status:      k.Status,
		IsDeleted:   service.IsDeletedFalse,
	}
	return g.db.WithContext(ctx).Create(row).Error
}

// DeleteAPIKey 软删除 API 凭证。
func (g *GormStore) DeleteAPIKey(ctx context.Context, spaceID, apiKeyID string) error {
	res := g.db.WithContext(ctx).
		Model(&service.APIKey{}).
		Where("c_space_id = ? AND c_api_key_id = ? AND "+notDeleted(), spaceID, apiKeyID).
		Update("c_is_deleted", service.IsDeletedTrue)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}

// ListAPIKeys 查询账户的 API 凭证（脱敏：api_key 截断，secret/passphrase 置空）。
func (g *GormStore) ListAPIKeys(ctx context.Context, spaceID, accountID string) ([]*service.APIKey, error) {
	var rows []*service.APIKey
	if err := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_account_id = ? AND "+notDeleted(), spaceID, accountID).
		Order("c_ctime DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, k := range rows {
		k.APIKey = crypto.MaskAPIKey(k.APIKey)
		k.APISecret = ""
		k.Passphrase = ""
		k.PermissionsRaw = parsePermissions(k.Permissions)
	}
	return rows, nil
}

// GetAPIKey 查询单个凭证并解密（供适配层下单使用，返回明文）。
func (g *GormStore) GetAPIKey(ctx context.Context, spaceID, apiKeyID string) (*service.APIKey, error) {
	var k service.APIKey
	if err := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_api_key_id = ? AND "+notDeleted(), spaceID, apiKeyID).
		First(&k).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, service.ErrNotFound
		}
		return nil, err
	}
	if g.encryptionKey != "" {
		secret, err := crypto.AESDecrypt(k.APISecret, g.encryptionKey)
		if err != nil {
			return nil, err
		}
		k.APISecret = secret
		if k.Passphrase != "" {
			pass, err := crypto.AESDecrypt(k.Passphrase, g.encryptionKey)
			if err != nil {
				return nil, err
			}
			k.Passphrase = pass
		}
	}
	k.PermissionsRaw = parsePermissions(k.Permissions)
	return &k, nil
}

// parsePermissions 解析 c_permissions JSON 字符串为切片。
func parsePermissions(raw string) []string {
	if raw == "" {
		return nil
	}
	var perms []string
	_ = json.Unmarshal([]byte(raw), &perms)
	return perms
}
