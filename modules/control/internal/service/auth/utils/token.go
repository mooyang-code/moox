package utils

import (
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/common/crypto"
)

// 导出 common/crypto 中的类型和函数，方便使用
type (
	TokenType     = crypto.TokenType
	UnifiedClaims = crypto.UnifiedClaims
	JWTConfig     = crypto.JWTConfig
)

var DefaultJWTConfig = crypto.DefaultJWTConfig

const (
	TokenTypeAccess       = crypto.TokenTypeAccess
	TokenTypeRefresh      = crypto.TokenTypeRefresh
	TokenTypeFileDownload = crypto.TokenTypeFileDownload
)

// GenerateToken 生成统一JWT令牌（复用 crypto 包）
func GenerateToken(claims *UnifiedClaims, secretKey string, expireDuration time.Duration) (string, error) {
	return crypto.GenerateToken(claims, secretKey, expireDuration)
}

// GenerateAccessToken 生成API访问token
func GenerateAccessToken(userID, username string, role int32, secretKey string, expireDuration time.Duration) (string, error) {
	return crypto.GenerateAccessToken(userID, username, role, secretKey, expireDuration)
}

// ParseToken 解析JWT令牌（复用 crypto 包）
func ParseToken(tokenString, secretKey string) (*UnifiedClaims, error) {
	return crypto.ParseToken(tokenString, secretKey)
}

// ValidateAccessToken 验证API访问token（复用 crypto 包）
func ValidateAccessToken(tokenString, secretKey string) (*UnifiedClaims, error) {
	return crypto.ValidateAccessToken(tokenString, secretKey)
}

// ExtractUserIDFromToken 从token中提取用户ID（不验证token类型）
func ExtractUserIDFromToken(tokenString string) (string, error) {
	// 先尝试使用默认密钥解析
	claims, err := ParseToken(tokenString, DefaultJWTConfig.SecretKey)
	if err != nil {
		return "", err
	}

	return claims.UserID, nil
}
