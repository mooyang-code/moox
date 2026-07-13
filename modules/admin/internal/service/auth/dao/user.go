package dao

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/softdelete"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserDAO 用户数据访问层
type UserDAO struct {
	db    *gorm.DB
	cache *CacheDB
}

// NewUserDAO 创建用户数据访问层
func NewUserDAO(db *gorm.DB, cache *CacheDB) *UserDAO {
	return &UserDAO{
		db:    db,
		cache: cache,
	}
}

// ===== 用户基础操作 =====

// CreateUser 创建用户
func (d *UserDAO) CreateUser(ctx context.Context, user *model.User) error {
	if user.UserID == "" {
		user.UserID = uuid.New().String()
	}
	user.CreatedAt = time.Now()
	user.UpdatedAt = time.Now()

	return d.db.WithContext(ctx).Create(user).Error
}

// GetUserByID 根据用户ID获取用户信息
func (d *UserDAO) GetUserByID(ctx context.Context, userID string) (*model.User, error) {
	var user model.User
	err := d.db.WithContext(ctx).
		Where("c_user_id = ? AND c_is_deleted = ?", userID, softdelete.IsDeletedFalse).
		First(&user).Error

	if err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUserByUsername 根据用户名获取用户信息
func (d *UserDAO) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	if err := d.db.WithContext(ctx).
		Where("c_username = ? AND c_is_deleted = ?", username, softdelete.IsDeletedFalse).
		First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateUser 更新用户信息
func (d *UserDAO) UpdateUser(ctx context.Context, userID string, updates map[string]interface{}) error {
	updates["c_mtime"] = time.Now()

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_is_deleted = ?", userID, softdelete.IsDeletedFalse).
		Updates(updates).Error
}

// UpdateUserPassword 更新用户密码
func (d *UserDAO) UpdateUserPassword(ctx context.Context, userID, passwordHash string) error {
	updates := map[string]interface{}{
		"c_password_hash":        passwordHash,
		"c_last_password_change": time.Now(),
		"c_mtime":                time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_is_deleted = ?", userID, softdelete.IsDeletedFalse).
		Updates(updates).Error
}

// UpdateUserLoginInfo 更新用户登录信息
func (d *UserDAO) UpdateUserLoginInfo(ctx context.Context, userID, clientIP string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"c_last_login_at": &now,
		"c_last_login_ip": clientIP,
		"c_mtime":         now,
	}

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_is_deleted = ?", userID, softdelete.IsDeletedFalse).
		Updates(updates).Error
}

// ===== 登录历史操作 =====

// CreateLoginHistory 创建登录历史记录
func (d *UserDAO) CreateLoginHistory(ctx context.Context, history *model.LoginHistory) error {
	history.CreatedAt = time.Now()

	return d.db.WithContext(ctx).Create(history).Error
}

// ===== 缓存操作 =====

// SetLoginSalt 设置登录盐值
func (d *UserDAO) SetLoginSalt(ctx context.Context, username string, salt model.LoginSalt) error {
	key := fmt.Sprintf("login_salt:%s", username)
	data, err := json.Marshal(salt)
	if err != nil {
		return err
	}

	ttl := salt.ExpiresAt.Sub(time.Now())
	return d.cache.Set(ctx, key, string(data), ttl)
}

// GetLoginSalt 获取登录盐值
func (d *UserDAO) GetLoginSalt(ctx context.Context, username string) (*model.LoginSalt, error) {
	key := fmt.Sprintf("login_salt:%s", username)
	data, err := d.cache.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	var salt model.LoginSalt
	err = json.Unmarshal([]byte(data), &salt)
	if err != nil {
		return nil, err
	}
	return &salt, nil
}

func (d *UserDAO) ConsumeLoginSalt(ctx context.Context, username string) (*model.LoginSalt, error) {
	data, err := d.cache.consume(ctx, fmt.Sprintf("login_salt:%s", username))
	if err != nil {
		return nil, err
	}
	var salt model.LoginSalt
	if err := json.Unmarshal([]byte(data), &salt); err != nil {
		return nil, err
	}
	return &salt, nil
}

func (d *UserDAO) SetSigningSession(ctx context.Context, session model.RequestSigningSession) error {
	return d.setExpiringJSON(ctx, "signing_session:"+session.SessionID, session, session.ExpiresAt)
}

func (d *UserDAO) GetSigningSession(ctx context.Context, sessionID string) (*model.RequestSigningSession, error) {
	data, err := d.cache.Get(ctx, "signing_session:"+sessionID)
	if err != nil {
		return nil, err
	}
	var session model.RequestSigningSession
	if err := json.Unmarshal([]byte(data), &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (d *UserDAO) DeleteSigningSession(ctx context.Context, sessionID string) error {
	return d.cache.Delete(ctx, "signing_session:"+sessionID)
}

func (d *UserDAO) ConsumeSessionNonce(ctx context.Context, sessionID, nonce string, ttl time.Duration) (bool, error) {
	return d.cache.SetIfAbsent(ctx, fmt.Sprintf("session_nonce:%s:%s", sessionID, nonce), "1", ttl)
}

func (d *UserDAO) SetRawSessionTicket(ctx context.Context, ticket model.RawSessionTicket) error {
	return d.setExpiringJSON(ctx, "raw_ticket:"+ticket.TicketID, ticket, ticket.ExpiresAt)
}

func (d *UserDAO) ConsumeRawSessionTicket(ctx context.Context, ticketID string) (*model.RawSessionTicket, error) {
	data, err := d.cache.consume(ctx, "raw_ticket:"+ticketID)
	if err != nil {
		return nil, err
	}
	var ticket model.RawSessionTicket
	if err := json.Unmarshal([]byte(data), &ticket); err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (d *UserDAO) setExpiringJSON(ctx context.Context, key string, value any, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return fmt.Errorf("cache value expiration must be in the future")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return d.cache.Set(ctx, key, string(data), ttl)
}

// SetLoginAttempt 设置登录尝试记录
func (d *UserDAO) SetLoginAttempt(ctx context.Context, username, ip string, attempt model.LoginAttempt) error {
	key := fmt.Sprintf("login_attempt:%s:%s", username, ip)
	data, err := json.Marshal(attempt)
	if err != nil {
		return err
	}

	ttl := attempt.ExpiresAt.Sub(time.Now())
	if ttl <= 0 {
		ttl = 30 * time.Minute // 默认30分钟
	}

	return d.cache.Set(ctx, key, string(data), ttl)
}

// GetLoginAttempt 获取登录尝试记录
func (d *UserDAO) GetLoginAttempt(ctx context.Context, username, ip string) (*model.LoginAttempt, error) {
	key := fmt.Sprintf("login_attempt:%s:%s", username, ip)
	data, err := d.cache.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	var attempt model.LoginAttempt
	err = json.Unmarshal([]byte(data), &attempt)
	if err != nil {
		return nil, err
	}

	return &attempt, nil
}

// DeleteLoginAttempt 删除登录尝试记录
func (d *UserDAO) DeleteLoginAttempt(ctx context.Context, username, ip string) error {
	key := fmt.Sprintf("login_attempt:%s:%s", username, ip)
	return d.cache.Del(ctx, key)
}
