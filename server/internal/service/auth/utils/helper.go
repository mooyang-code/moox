package utils

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"

	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go"
)

// GetUserInfoFromCtx 从trpc上下文元数据中获取用户信息
func GetUserInfoFromCtx(ctx context.Context) (userID string, username string, role int32, err error) {
	// 获取用户ID
	userIDBytes := trpc.GetMetaData(ctx, model.CtxUserID)
	userID = string(userIDBytes)
	if userID == "" {
		return "", "", 0, fmt.Errorf("用户ID未在上下文中找到")
	}

	// 获取用户名
	usernameBytes := trpc.GetMetaData(ctx, model.CtxUsername)
	username = string(usernameBytes)

	// 获取用户角色
	roleBytes := trpc.GetMetaData(ctx, model.CtxUserRole)
	roleStr := string(roleBytes)
	if roleStr != "" {
		if roleInt, parseErr := strconv.ParseInt(roleStr, 10, 32); parseErr == nil {
			role = int32(roleInt)
		}
	}
	return userID, username, role, nil
}

// BuildSafeUserInfo 构造安全的用户信息（防止XSS攻击）
func BuildSafeUserInfo(user *model.User) *pb.UserInfo {
	var lastLoginAt int64
	if user.LastLoginAt != nil {
		lastLoginAt = user.LastLoginAt.Unix()
	}

	return &pb.UserInfo{
		UserId:      user.UserID, // UserID通常是系统生成的，不需要转义
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

// ValidateStringFormat 验证字符串格式（规则：长度1-20，仅支持大小写字母和数字）
func ValidateStringFormat(value, fieldName string) error {
	// 检查长度
	if len(value) < 1 || len(value) > 20 {
		return fmt.Errorf("%s长度必须在1-20个字符之间", fieldName)
	}

	// 检查字符类型（仅支持大小写字母和数字）
	matched, err := regexp.MatchString("^[a-zA-Z0-9]+$", value)
	if err != nil {
		return fmt.Errorf("验证%s格式时发生错误", fieldName)
	}

	if !matched {
		return fmt.Errorf("%s只能包含大小写字母和数字", fieldName)
	}
	return nil
}
