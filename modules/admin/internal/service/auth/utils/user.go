package utils

import "context"

// UserContextKey 用户信息在context中的key
const UserContextKey = "user_id"

// GetUserIDFromContext 从context中获取用户ID
func GetUserIDFromContext(ctx context.Context) string {
	if userID := ctx.Value(UserContextKey); userID != nil {
		if uid, ok := userID.(string); ok {
			return uid
		}
	}
	return "anonymous_user"
}
