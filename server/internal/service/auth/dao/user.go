package dao

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/auth/model"

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
		Where("c_user_id = ? AND c_invalid != 1", userID).
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
		Where("c_username = ? AND c_invalid != 1", username).
		First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByEmail 根据邮箱获取用户信息
func (d *UserDAO) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	err := d.db.WithContext(ctx).
		Where("c_email = ? AND c_invalid != 1", email).
		First(&user).Error

	if err != nil {
		return nil, err
	}

	return &user, nil
}

// UpdateUser 更新用户信息
func (d *UserDAO) UpdateUser(ctx context.Context, userID string, updates map[string]interface{}) error {
	updates["c_mtime"] = time.Now()

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_invalid != 1", userID).
		Updates(updates).Error
}

// UpdateUserPassword 更新用户密码
func (d *UserDAO) UpdateUserPassword(ctx context.Context, userID, passwordHash, salt string) error {
	updates := map[string]interface{}{
		"c_password_hash":        passwordHash,
		"c_salt":                 salt,
		"c_last_password_change": time.Now(),
		"c_mtime":                time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_invalid != 1", userID).
		Updates(updates).Error
}

// UpdateUserLoginInfo 更新用户登录信息
func (d *UserDAO) UpdateUserLoginInfo(ctx context.Context, userID, clientIP string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"c_last_login_at":  &now,
		"c_last_login_ip":  clientIP,
		"c_login_attempts": 0,   // 成功登录后重置尝试次数
		"c_locked_until":   nil, // 清除锁定状态
		"c_mtime":          now,
	}

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_invalid != 1", userID).
		Updates(updates).Error
}

// IncrementLoginAttempts 增加登录尝试次数
func (d *UserDAO) IncrementLoginAttempts(ctx context.Context, userID string, lockUntil *time.Time) error {
	updates := map[string]interface{}{
		"c_login_attempts": gorm.Expr("c_login_attempts + 1"),
		"c_mtime":          time.Now(),
	}

	if lockUntil != nil {
		updates["c_locked_until"] = lockUntil
	}

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_invalid != 1", userID).
		Updates(updates).Error
}

// DeleteUser 软删除用户
func (d *UserDAO) DeleteUser(ctx context.Context, userID string) error {
	updates := map[string]interface{}{
		"c_invalid": 1,
		"c_mtime":   time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.User{}).
		Where("c_user_id = ? AND c_invalid != 1", userID).
		Updates(updates).Error
}

// ===== 令牌操作 =====

// CreateToken 创建令牌记录
func (d *UserDAO) CreateToken(ctx context.Context, token *model.ActiveToken) error {
	token.CreatedAt = time.Now()
	token.UpdatedAt = time.Now()

	return d.db.WithContext(ctx).Create(token).Error
}

// GetTokenByJTI 根据JTI获取令牌信息
func (d *UserDAO) GetTokenByJTI(ctx context.Context, jti string) (*model.ActiveToken, error) {
	var token model.ActiveToken
	err := d.db.WithContext(ctx).
		Where("c_jti = ? AND c_invalid != 1 AND c_revoked = 0", jti).
		First(&token).Error

	if err != nil {
		return nil, err
	}

	return &token, nil
}

// UpdateTokenLastUsed 更新令牌最后使用时间
func (d *UserDAO) UpdateTokenLastUsed(ctx context.Context, jti string) error {
	updates := map[string]interface{}{
		"c_last_used_at": time.Now(),
		"c_mtime":        time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.ActiveToken{}).
		Where("c_jti = ? AND c_invalid != 1", jti).
		Updates(updates).Error
}

// RevokeToken 撤销令牌
func (d *UserDAO) RevokeToken(ctx context.Context, jti string) error {
	updates := map[string]interface{}{
		"c_revoked": 1,
		"c_mtime":   time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.ActiveToken{}).
		Where("c_jti = ? AND c_invalid != 1", jti).
		Updates(updates).Error
}

// RevokeUserTokens 撤销用户的所有令牌
func (d *UserDAO) RevokeUserTokens(ctx context.Context, userID string) error {
	updates := map[string]interface{}{
		"c_revoked": 1,
		"c_mtime":   time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.ActiveToken{}).
		Where("c_user_id = ? AND c_invalid != 1 AND c_revoked = 0", userID).
		Updates(updates).Error
}

// CleanExpiredTokens 清理过期的令牌
func (d *UserDAO) CleanExpiredTokens(ctx context.Context) error {
	updates := map[string]interface{}{
		"c_invalid": 1,
		"c_mtime":   time.Now(),
	}

	return d.db.WithContext(ctx).
		Model(&model.ActiveToken{}).
		Where("c_expires_at < ? AND c_invalid != 1", time.Now()).
		Updates(updates).Error
}

// ===== 登录历史操作 =====

// CreateLoginHistory 创建登录历史记录
func (d *UserDAO) CreateLoginHistory(ctx context.Context, history *model.LoginHistory) error {
	history.CreatedAt = time.Now()

	return d.db.WithContext(ctx).Create(history).Error
}

// GetLoginHistoryByUser 获取用户登录历史
func (d *UserDAO) GetLoginHistoryByUser(ctx context.Context, userID string, limit int) ([]*model.LoginHistory, error) {
	var histories []*model.LoginHistory

	query := d.db.WithContext(ctx).
		Where("c_user_id = ?", userID).
		Order("c_ctime DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	err := query.Find(&histories).Error
	return histories, err
}

// ===== 用户操作日志 =====

// CreateUserAction 创建用户操作日志
func (d *UserDAO) CreateUserAction(ctx context.Context, action *model.UserAction) error {
	action.CreatedAt = time.Now()

	return d.db.WithContext(ctx).Create(action).Error
}

// GetUserActionsByUser 获取用户操作日志
func (d *UserDAO) GetUserActionsByUser(ctx context.Context, userID string, limit int) ([]*model.UserAction, error) {
	var actions []*model.UserAction

	query := d.db.WithContext(ctx).
		Where("c_user_id = ?", userID).
		Order("c_ctime DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	err := query.Find(&actions).Error
	return actions, err
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

// SetChangePasswordSalt 设置修改密码盐值
func (d *UserDAO) SetChangePasswordSalt(ctx context.Context, userID string, salt model.ChangePasswordSalt) error {
	key := fmt.Sprintf("change_pwd_salt:%s", userID)
	data, err := json.Marshal(salt)
	if err != nil {
		return err
	}

	ttl := salt.ExpiresAt.Sub(time.Now())
	return d.cache.Set(ctx, key, string(data), ttl)
}

// GetChangePasswordSalt 获取修改密码盐值
func (d *UserDAO) GetChangePasswordSalt(ctx context.Context, userID string) (*model.ChangePasswordSalt, error) {
	key := fmt.Sprintf("change_pwd_salt:%s", userID)
	data, err := d.cache.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	var salt model.ChangePasswordSalt
	err = json.Unmarshal([]byte(data), &salt)
	if err != nil {
		return nil, err
	}

	return &salt, nil
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

// ===== 统计查询 =====

// CountUsers 统计用户数量
func (d *UserDAO) CountUsers(ctx context.Context, status int32) (int64, error) {
	var count int64
	query := d.db.WithContext(ctx).Model(&model.User{}).Where("c_invalid != 1")

	if status >= 0 {
		query = query.Where("c_status = ?", status)
	}

	err := query.Count(&count).Error
	return count, err
}

// CountActiveTokens 统计活跃令牌数量
func (d *UserDAO) CountActiveTokens(ctx context.Context, userID string) (int64, error) {
	var count int64
	query := d.db.WithContext(ctx).
		Model(&model.ActiveToken{}).
		Where("c_invalid != 1 AND c_revoked = 0 AND c_expires_at > ?", time.Now())

	if userID != "" {
		query = query.Where("c_user_id = ?", userID)
	}

	err := query.Count(&count).Error
	return count, err
}

// GetUsersByRole 根据角色获取用户列表
func (d *UserDAO) GetUsersByRole(ctx context.Context, role int32, limit, offset int) ([]*model.User, error) {
	var users []*model.User

	query := d.db.WithContext(ctx).
		Where("c_role = ? AND c_invalid != 1", role).
		Order("c_ctime DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	if offset > 0 {
		query = query.Offset(offset)
	}

	err := query.Find(&users).Error
	return users, err
}

// ===== 初始化方法 =====

// AutoMigrate 自动迁移表结构
//
// 作用：根据 Go struct 定义自动创建或更新数据库表结构
//
// 功能：
//   - 首次运行：如果表不存在，自动创建表
//   - 字段新增：如果 struct 新增字段，自动添加列到表中
//   - 字段修改：如果字段类型改变，尝试修改列类型
//   - 索引创建：根据 struct tag 自动创建索引
//   - 安全保护：不会删除表中已存在但 struct 中不存在的列
//
// 注意：这不是数据迁移，而是表结构（schema）的自动管理
func (d *UserDAO) AutoMigrate() error {
	return d.db.AutoMigrate(
		&model.User{},
		&model.ActiveToken{},
		&model.LoginHistory{},
		&model.UserAction{},
	)
}

// Close 关闭连接
func (d *UserDAO) Close() error {
	if d.cache != nil {
		return d.cache.Close()
	}
	return nil
}
