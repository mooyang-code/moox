package auth

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// UserDAO 用户数据访问层。用户由部署命令写入，这里只读用户和更新登录信息。
type UserDAO struct {
	db *gorm.DB
}

// NewUserDAO 创建用户数据访问层。
func NewUserDAO(db *gorm.DB) *UserDAO {
	return &UserDAO{db: db}
}

// GetUserByID 根据用户 ID 获取用户信息。
func (d *UserDAO) GetUserByID(ctx context.Context, userID string) (*User, error) {
	var user User
	if err := d.db.WithContext(ctx).Where("c_user_id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByUsername 根据用户名获取用户信息。
func (d *UserDAO) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	var user User
	if err := d.db.WithContext(ctx).Where("c_username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateUserLoginInfo 更新用户登录信息。
func (d *UserDAO) UpdateUserLoginInfo(ctx context.Context, userID, clientIP string) error {
	now := time.Now()
	updates := map[string]any{
		"c_last_login_at": &now,
		"c_last_login_ip": clientIP,
		"c_mtime":         now,
	}
	return d.db.WithContext(ctx).
		Model(&User{}).
		Where("c_user_id = ?", userID).
		Updates(updates).Error
}
