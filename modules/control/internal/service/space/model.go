package space

import "time"

// Space 是管理台的业务隔离空间，对应 A 股、美股、Crypto 等最大业务上下文。
type Space struct {
	ID          int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	SpaceID     string    `gorm:"column:c_space_id;not null;uniqueIndex:idx_spaces_space_id_invalid" json:"space_id"`
	Name        string    `gorm:"column:c_name;not null" json:"name"`
	Description string    `gorm:"column:c_description;not null;default:''" json:"description"`
	Owner       string    `gorm:"column:c_owner;not null;default:''" json:"owner"`
	Market      string    `gorm:"column:c_market;not null;default:''" json:"market"`
	Timezone    string    `gorm:"column:c_timezone;not null;default:''" json:"timezone"`
	Status      string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Attributes  string    `gorm:"column:c_attributes;not null;default:'{}'" json:"attributes"`
	Invalid     int       `gorm:"column:c_invalid;not null;default:0;uniqueIndex:idx_spaces_space_id_invalid" json:"-"`
	CreatedAt   time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName 返回 Space 的权威 SQL 表名。
func (Space) TableName() string { return "t_spaces" }

// SpaceMember 描述用户在 Space 内的角色与状态。
type SpaceMember struct {
	ID         int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	SpaceID    string    `gorm:"column:c_space_id;not null;uniqueIndex:idx_space_members_space_user" json:"space_id"`
	UserID     string    `gorm:"column:c_user_id;not null;uniqueIndex:idx_space_members_space_user" json:"user_id"`
	Role       string    `gorm:"column:c_role;not null;default:'member'" json:"role"`
	Status     string    `gorm:"column:c_status;not null;default:'active'" json:"status"`
	Attributes string    `gorm:"column:c_attributes;not null;default:'{}'" json:"attributes"`
	CreatedAt  time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName 返回 SpaceMember 的权威 SQL 表名。
func (SpaceMember) TableName() string { return "t_space_members" }
