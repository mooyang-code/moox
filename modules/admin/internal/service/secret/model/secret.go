package model

import "time"

// Secret 秘钥管理表
type Secret struct {
	ID           int       `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// SpaceID 预留多租户隔离字段，当前为全局共享模式，默认空字符串。
	// 未来如需空间隔离，在 DAO 层的 Create/List/Get/Update/Delete 中读写并过滤此字段。
	SpaceID      string    `gorm:"column:c_space_id;not null;default:''" json:"space_id"`
	SecretID     string    `gorm:"column:c_secret_id;not null" json:"secret_id"`
	Name         string    `gorm:"column:c_name;not null" json:"name"`
	Description  string    `gorm:"column:c_description;not null;default:''" json:"description"`
	Category     string    `gorm:"column:c_category;not null" json:"category"`
	Provider     string    `gorm:"column:c_provider;not null;default:''" json:"provider"`
	SecretType   string    `gorm:"column:c_secret_type;not null;default:'api_key'" json:"secret_type"`
	KeyID        string    `gorm:"column:c_key_id;not null;default:''" json:"key_id"`
	SecretValue  string    `gorm:"column:c_secret_value;not null" json:"secret_value"`
	ExtraConfig  string    `gorm:"column:c_extra_config;not null;default:'{}'" json:"extra_config"`
	Status       string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	LastUsedAt   *time.Time `gorm:"column:c_last_used_at" json:"last_used_at"`
	LastUsedBy   string    `gorm:"column:c_last_used_by;not null;default:''" json:"last_used_by"`
	Creator      string    `gorm:"column:c_creator;not null;default:''" json:"creator"`
	IsDeleted    string    `gorm:"column:c_is_deleted;not null;default:'false'" json:"is_deleted"`
	CreateTime   time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	ModifyTime   time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

func (s *Secret) TableName() string {
	return "t_secrets"
}
