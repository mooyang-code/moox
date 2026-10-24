package model

import (
	"time"
)

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

// LoginSalt 登录盐值缓存 (用于BadgerDB)
type LoginSalt struct {
	Username  string    `json:"username"`
	Salt      string    `json:"salt"`
	Timestamp int64     `json:"timestamp"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RequestSigningSession struct {
	SessionID       string    `json:"session_id"`
	UserID          string    `json:"user_id"`
	EncryptedSecret string    `json:"encrypted_secret"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type RawSessionTicket struct {
	TicketID          string    `json:"ticket_id"`
	SessionID         string    `json:"session_id"`
	ResourceSessionID string    `json:"resource_session_id"`
	UserID            string    `json:"user_id"`
	Operation         string    `json:"operation"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// LoginAttempt 登录尝试记录 (用于BadgerDB)
type LoginAttempt struct {
	Username  string    `json:"username"`
	IP        string    `json:"ip"`
	Attempts  int       `json:"attempts"`
	LockedAt  time.Time `json:"locked_at"`
	ExpiresAt time.Time `json:"expires_at"`
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
