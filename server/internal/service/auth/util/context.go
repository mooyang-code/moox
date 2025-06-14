package util

import (
	"context"
	"fmt"
)

// GetUserInfoFromContext 从上下文中获取用户信息
func GetUserInfoFromContext(ctx context.Context) (userID string, username string, role int32, err error) {
	userIDVal := ctx.Value("user_id")
	if userIDVal == nil {
		return "", "", 0, fmt.Errorf("用户ID未在上下文中找到")
	}
	userID, ok := userIDVal.(string)
	if !ok || userID == "" {
		return "", "", 0, fmt.Errorf("无效的用户ID")
	}

	usernameVal := ctx.Value("username")
	if usernameVal != nil {
		username, _ = usernameVal.(string)
	}

	roleVal := ctx.Value("user_role")
	if roleVal != nil {
		role, _ = roleVal.(int32)
	}

	return userID, username, role, nil
}
