package model

import (
	"strings"
	"time"
)

// 主机的 SSH 登录凭据（密码或私钥）由 SSH 服务按主机维护，不经过通用的 SecretMgr API。
// 秘钥 ID 为 SSHHostIDPrefix + 主机 ID，c_key_id 存登录用户名。
const (
	SSHHostIDPrefix = "moox:ssh-host:"
	SSHCategory     = "ssh"
)

// SSHHostSecretID 返回主机 SSH 凭据的秘钥 ID。
func SSHHostSecretID(hostID string) string { return SSHHostIDPrefix + hostID }

// IsSystem 判断秘钥是否由系统内部服务维护（主机 SSH 凭据），通用秘钥接口看不到也改不了。
func (s *Secret) IsSystem() bool {
	return strings.HasPrefix(s.SecretID, SSHHostIDPrefix)
}

// EventBus 内部凭据（各角色令牌）的分类与提供方，由 eventbus-credentials ensure 生成，按 key_id 读取。
const (
	EventBusCategory = "eventbus"
	EventBusProvider = "moox_eventbus"
)

// EnvironmentBound 判断秘钥是否属于本环境（主机 SSH 凭据、EventBus 内部凭据）：
// 这些秘钥由部署工具生成并按固定标识读取，迁移到另一套环境会和那里自己的同名凭据冲突，所以不导出也不导入。
func (s *Secret) EnvironmentBound() bool {
	return s.IsSystem() || (s.Category == EventBusCategory && s.Provider == EventBusProvider)
}

// setup init 管理的固定初始云账号密钥标识。
const TencentDefaultSecretID = "tencent-default"

// Secret 秘钥管理表（物理硬删除，c_secret_value 与 c_extra_config 均 AES-GCM 加密存储）。
type Secret struct {
	ID          int       `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	SecretID    string    `gorm:"column:c_secret_id;not null" json:"secret_id"`
	Name        string    `gorm:"column:c_name;not null" json:"name"`
	Description string    `gorm:"column:c_description;not null;default:''" json:"description"`
	Category    string    `gorm:"column:c_category;not null" json:"category"`
	Provider    string    `gorm:"column:c_provider;not null;default:''" json:"provider"`
	SecretType  string    `gorm:"column:c_secret_type;not null;default:'api_key'" json:"secret_type"`
	KeyID       string    `gorm:"column:c_key_id;not null;default:''" json:"key_id"`
	SecretValue string    `gorm:"column:c_secret_value;not null" json:"secret_value"`
	ExtraConfig string    `gorm:"column:c_extra_config;not null;default:''" json:"extra_config"`
	Status      string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Creator     string    `gorm:"column:c_creator;not null;default:''" json:"creator"`
	CreateTime  time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	ModifyTime  time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

func (s *Secret) TableName() string {
	return "t_secrets"
}
