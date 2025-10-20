package utils

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// UserContextKey 用户信息在context中的key
const UserContextKey = "user_id"

// ExtractUserMiddleware 从请求头中提取用户信息的中间件
func ExtractUserMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := extractUserID(c)

		// 将用户ID添加到context中
		ctx := context.WithValue(c.Request.Context(), UserContextKey, userID)
		c.Request = c.Request.WithContext(ctx)

		log.InfoContextf(c.Request.Context(), "[Auth] 提取到用户ID: %s", userID)
		c.Next()
	}
}

// extractUserID 提取用户ID的核心逻辑
func extractUserID(c *gin.Context) string {
	// 1. 尝试从Authorization header解析
	if userID := extractFromAuthorizationHeader(c); userID != "" {
		log.InfoContextf(c.Request.Context(), "[Auth] 从Authorization header提取到用户ID: %s", userID)
		return userID
	}

	// 3. 尝试从X-User-ID header获取
	if userID := c.GetHeader("X-User-ID"); userID != "" {
		log.InfoContextf(c.Request.Context(), "[Auth] 从X-User-ID header提取到用户ID: %s", userID)
		return userID
	}

	// 4. 使用默认值 - 记录所有header用于调试
	authHeader := c.GetHeader("Authorization")
	accessToken := c.GetHeader("X-Access-Token")
	userIDHeader := c.GetHeader("X-User-ID")
	log.WarnContextf(c.Request.Context(), "[Auth] 无法获取用户ID，使用默认值。Headers: Authorization=%s, X-Access-Token=%s, X-User-ID=%s",
		truncateToken(authHeader), truncateToken(accessToken), userIDHeader)
	return "anonymous_user"
}

// truncateToken 截断token用于日志记录
func truncateToken(token string) string {
	if token == "" {
		return "<empty>"
	}
	if len(token) > 20 {
		return token[:20] + "..."
	}
	return token
}

// extractFromAuthorizationHeader 从Authorization header中提取用户ID
func extractFromAuthorizationHeader(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		return ""
	}

	authType := strings.ToLower(parts[0])
	token := parts[1]

	switch authType {
	case "bearer":
		// JWT token方式
		if userID, err := ExtractUserIDFromToken(token); err == nil {
			return userID
		} else {
			log.WarnContextf(c.Request.Context(), "[Auth] JWT解析失败: %v", err)
		}

	case "user":
		// 直接传递用户ID方式（开发环境）
		return token
	}

	return ""
}

// GetUserIDFromContext 从context中获取用户ID
func GetUserIDFromContext(ctx context.Context) string {
	if userID := ctx.Value(UserContextKey); userID != nil {
		if uid, ok := userID.(string); ok {
			return uid
		}
	}
	return "anonymous_user"
}
