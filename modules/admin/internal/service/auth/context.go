package auth

import (
	"context"
	"fmt"
	"html"
	"regexp"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"trpc.group/trpc-go/trpc-go"
)

var validIdentifierPattern = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

// GetUserInfoFromCtx 从 trpc 上下文元数据中获取已登录用户。系统只有一个超级用户，没有角色。
func GetUserInfoFromCtx(ctx context.Context) (userID string, username string, err error) {
	userID = string(trpc.GetMetaData(ctx, CtxUserID))
	if userID == "" {
		return "", "", fmt.Errorf("用户ID未在上下文中找到")
	}
	username = string(trpc.GetMetaData(ctx, CtxUsername))
	return userID, username, nil
}

// BuildSafeUserInfo 构造安全的用户信息，避免把用户输入直接写入响应。
func BuildSafeUserInfo(user *User) *pb.UserInfo {
	var lastLoginAt int64
	if user.LastLoginAt != nil {
		lastLoginAt = user.LastLoginAt.UnixMilli()
	}

	return &pb.UserInfo{
		UserId:      user.UserID,
		Username:    html.EscapeString(user.Username),
		CreatedAt:   user.CreatedAt.UnixMilli(),
		LastLoginAt: lastLoginAt,
		LastLoginIp: html.EscapeString(user.LastLoginIP),
	}
}

// ValidateStringFormat 验证字符串格式：长度 1-20，仅支持大小写字母和数字。
func ValidateStringFormat(value, fieldName string) error {
	if len(value) < 1 || len(value) > 20 {
		return fmt.Errorf("%s长度必须在1-20个字符之间", fieldName)
	}
	if !validIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s只能包含大小写字母和数字", fieldName)
	}
	return nil
}
