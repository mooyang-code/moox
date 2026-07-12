package utils

import (
	"html"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// BuildSafeUserInfo 构造安全的用户信息，避免把用户输入直接写入响应。
func BuildSafeUserInfo(user *model.User) *pb.UserInfo {
	var lastLoginAt int64
	if user.LastLoginAt != nil {
		lastLoginAt = user.LastLoginAt.Unix()
	}

	return &pb.UserInfo{
		UserId:      user.UserID,
		Username:    html.EscapeString(user.Username),
		Nickname:    html.EscapeString(user.Nickname),
		Email:       html.EscapeString(user.Email),
		Avatar:      html.EscapeString(user.Avatar),
		Status:      pb.UserStatus(user.Status),
		Role:        pb.UserRole(user.Role),
		CreatedAt:   user.CreatedAt.Unix(),
		LastLoginAt: lastLoginAt,
		LastLoginIp: html.EscapeString(user.LastLoginIP),
	}
}
