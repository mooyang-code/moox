package model

import "time"

// User 用户数据模型 (对应 t_users 表)。系统只有一个超级用户，没有角色、状态与个人资料。
type User struct {
	ID           int64      `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	UserID       string     `gorm:"column:c_user_id;uniqueIndex;not null" json:"user_id"`
	Username     string     `gorm:"column:c_username;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"column:c_password_hash;not null" json:"-"`
	LastLoginAt  *time.Time `gorm:"column:c_last_login_at" json:"last_login_at"`
	LastLoginIP  string     `gorm:"column:c_last_login_ip;default:''" json:"last_login_ip"`
	CreatedAt    time.Time  `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName 表名
func (u *User) TableName() string {
	return "t_users"
}

// 登录日志在活动日志里的来源与类别。
const (
	LoginLogSource   = "admin"
	LoginLogCategory = "auth.login"
)

// 上下文元数据键常量
const (
	CtxUserID    = "X-User-ID"
	CtxUsername  = "X-User-Name"
	CtxSessionID = "X-Session-ID"
	CtxTraceID   = "X-Trace-ID"
)
