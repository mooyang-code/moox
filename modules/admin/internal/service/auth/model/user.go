package model

import (
	"time"
)

// User 用户数据模型 (对应 t_users 表)
type User struct {
	ID                 int64      `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	UserID             string     `gorm:"column:c_user_id;uniqueIndex;not null" json:"user_id"`
	Username           string     `gorm:"column:c_username;uniqueIndex;not null" json:"username"`
	PasswordHash       string     `gorm:"column:c_password_hash;not null" json:"-"`
	Salt               string     `gorm:"column:c_salt;not null" json:"-"`
	Nickname           string     `gorm:"column:c_nickname;default:''" json:"nickname"`
	Email              string     `gorm:"column:c_email;uniqueIndex;default:''" json:"email"`
	Avatar             string     `gorm:"column:c_avatar;default:''" json:"avatar"`
	Role               int32      `gorm:"column:c_role;not null;default:1" json:"role"`
	Status             int32      `gorm:"column:c_status;not null;default:1" json:"status"`
	LastLoginAt        *time.Time `gorm:"column:c_last_login_at" json:"last_login_at"`
	LastLoginIP        string     `gorm:"column:c_last_login_ip;default:''" json:"last_login_ip"`
	LastPasswordChange time.Time  `gorm:"column:c_last_password_change;default:CURRENT_TIMESTAMP" json:"last_password_change"`
	IsDeleted          bool       `gorm:"column:c_is_deleted;not null;default:false" json:"-"`
	CreatedAt          time.Time  `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:c_mtime;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName 表名
func (u *User) TableName() string {
	return "t_users"
}

// LoginHistory 登录历史模型 (对应 t_login_history 表)
type LoginHistory struct {
	ID              int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	UserID          string    `gorm:"column:c_user_id;not null;index" json:"user_id"`
	Username        string    `gorm:"column:c_username;not null" json:"username"`
	LoginType       string    `gorm:"column:c_login_type;not null;default:'password'" json:"login_type"`
	ClientIP        string    `gorm:"column:c_client_ip;not null;index" json:"client_ip"`
	UserAgent       string    `gorm:"column:c_user_agent;default:''" json:"user_agent"`
	DeviceID        string    `gorm:"column:c_device_id;default:''" json:"device_id"`
	Location        string    `gorm:"column:c_location;default:''" json:"location"`
	LoginResult     string    `gorm:"column:c_login_result;not null;index" json:"login_result"`
	FailureReason   string    `gorm:"column:c_failure_reason;default:''" json:"failure_reason"`
	SessionDuration int       `gorm:"column:c_session_duration;default:0" json:"session_duration"`
	CreatedAt       time.Time `gorm:"column:c_ctime;default:CURRENT_TIMESTAMP;index" json:"created_at"`
}

// TableName 表名
func (h *LoginHistory) TableName() string {
	return "t_login_history"
}

// LoginSalt 登录盐值缓存 (用于BadgerDB)
type LoginSalt struct {
	Username  string    `json:"username"`
	Salt      string    `json:"salt"`
	Timestamp int64     `json:"timestamp"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ChangePasswordSalt 修改密码盐值缓存 (用于BadgerDB)
type ChangePasswordSalt struct {
	UserID    string    `json:"user_id"`
	Salt      string    `json:"salt"`
	Timestamp int64     `json:"timestamp"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LoginAttempt 登录尝试记录 (用于BadgerDB)
type LoginAttempt struct {
	Username  string    `json:"username"`
	IP        string    `json:"ip"`
	Attempts  int       `json:"attempts"`
	LockedAt  time.Time `json:"locked_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// 用户状态枚举
const (
	UserStatusInactive  = 0 // 未激活
	UserStatusActive    = 1 // 正常
	UserStatusSuspended = 2 // 暂停
	UserStatusBanned    = 3 // 封禁
)

// 登录结果枚举
const (
	LoginResultSuccess = "success"
)

// 操作类型枚举
const (
	ActionRegister       = "register"
	ActionChangePassword = "change_password"
	ActionUpdateProfile  = "update_profile"
	ActionGetUserInfo    = "get_user_info"
)

// 上下文元数据键常量
const (
	CtxUserID   = "X-User-ID"
	CtxUsername = "X-User-Name"
	CtxUserRole = "X-User-Role"
	CtxTraceID  = "X-Trace-ID"
)
