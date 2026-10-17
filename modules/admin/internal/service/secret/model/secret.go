package model

import (
	"strings"
	"time"
)

// Gateway keyrings belong to the control plane, not the generic SecretMgr API.
const (
	GatewayKeyringType     = "gateway_keyring"
	GatewayKeyringIDPrefix = "moox:gateway-keyring:"
)

// ConfigBackupID 是配置备份（moox-cli 用管理员密码加密的 moox.toml）在 t_secrets 里的固定标识；整个系统只有一份，
// 属于本环境，秘钥迁移不导出也不导入它。
const ConfigBackupID = "moox-config-backup"

// SystemSpacePrefix 是系统内部空间的前缀：网关密钥环、EventBus 内部凭据等由部署工具在 mooxsys 空间里生成，属于本环境。
const SystemSpacePrefix = "mooxsys"

// EventBus 内部凭据（TLS 证书与各角色令牌）的分类与提供方，由 eventbus-credentials ensure 生成，按 key_id 读取。
const (
	EventBusCategory = "eventbus"
	EventBusProvider = "moox_eventbus"
)

// EnvironmentBound 判断秘钥是否属于本环境（系统空间里的、网关密钥环、EventBus 内部凭据、配置备份）：
// 这些秘钥由部署工具生成并按固定标识读取，迁移到另一套环境会和那里自己的同名凭据冲突，所以不导出也不导入。
func (s *Secret) EnvironmentBound() bool {
	return strings.HasPrefix(s.SpaceID, SystemSpacePrefix) || s.SecretID == ConfigBackupID ||
		s.SecretType == GatewayKeyringType || strings.HasPrefix(s.SecretID, GatewayKeyringIDPrefix) ||
		(s.Category == EventBusCategory && s.Provider == EventBusProvider)
}

// Secret 秘钥管理表
type Secret struct {
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// SpaceID 预留多租户隔离字段，当前为全局共享模式，默认空字符串。
	// 未来如需空间隔离，在 DAO 层的 Create/List/Get/Update/Delete 中读写并过滤此字段。
	SpaceID     string     `gorm:"column:c_space_id;not null;default:''" json:"space_id"`
	SecretID    string     `gorm:"column:c_secret_id;not null" json:"secret_id"`
	Name        string     `gorm:"column:c_name;not null" json:"name"`
	Description string     `gorm:"column:c_description;not null;default:''" json:"description"`
	Category    string     `gorm:"column:c_category;not null" json:"category"`
	Provider    string     `gorm:"column:c_provider;not null;default:''" json:"provider"`
	SecretType  string     `gorm:"column:c_secret_type;not null;default:'api_key'" json:"secret_type"`
	KeyID       string     `gorm:"column:c_key_id;not null;default:''" json:"key_id"`
	SecretValue string     `gorm:"column:c_secret_value;not null" json:"secret_value"`
	ExtraConfig string     `gorm:"column:c_extra_config;not null;default:'{}'" json:"extra_config"`
	Status      string     `gorm:"column:c_status;not null;default:'active'" json:"status"`
	LastUsedAt  *time.Time `gorm:"column:c_last_used_at" json:"last_used_at"`
	LastUsedBy  string     `gorm:"column:c_last_used_by;not null;default:''" json:"last_used_by"`
	Creator     string     `gorm:"column:c_creator;not null;default:''" json:"creator"`
	IsDeleted   bool       `gorm:"column:c_is_deleted;not null;default:false" json:"is_deleted"`
	CreateTime  time.Time  `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	ModifyTime  time.Time  `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

func (s *Secret) TableName() string {
	return "t_secrets"
}
