package utils

import (
	"fmt"
	"os"
	"time"

	authConfig "github.com/mooyang-code/moox/server/internal/service/auth/config"

	"github.com/golang-jwt/jwt/v5"
)

// TokenType token类型
type TokenType string

const (
	TokenTypeAccess       TokenType = "access"        // 用户API访问token
	TokenTypeRefresh      TokenType = "refresh"       // 刷新token
	TokenTypeFileDownload TokenType = "file_download" // 文件下载token
)

// UnifiedClaims 统一的JWT声明
type UnifiedClaims struct {
	UserID    string    `json:"user_id"`
	Username  string    `json:"username,omitempty"`  // API访问需要
	Role      int32     `json:"role,omitempty"`      // API访问需要
	FilePath  string    `json:"file_path,omitempty"` // 文件下载需要
	TokenType TokenType `json:"token_type"`          // token类型
	jwt.RegisteredClaims
}

// JWTConfig JWT配置
type JWTConfig struct {
	SecretKey string
	Issuer    string
}

// DefaultJWTConfig 默认JWT配置
var DefaultJWTConfig = JWTConfig{
	SecretKey: getJWTSecretKey(),
	Issuer:    getJWTIssuer(),
}

// getJWTSecretKey 获取JWT密钥（统一从配置文件读取）
func getJWTSecretKey() string {
	// 优先从配置文件读取（统一密钥源）
	if cfg, err := authConfig.LoadConfig(); err == nil {
		return cfg.JWT.SecretKey
	}

	// 兼容环境变量（用于生产环境覆盖）
	if secretKey := os.Getenv("MOOX_JWT_SECRET_KEY"); secretKey != "" {
		return secretKey
	}

	// 兜底：使用默认值（仅用于开发环境）
	return "kJ8#3Lz!b1A6xQwP2dR9vM4nS7eT0uYpG5hZcV8jF2mB6sXlD3rWqN0tH9uK1oE4"
}

// getJWTIssuer 获取JWT颁发者
func getJWTIssuer() string {
	if issuer := os.Getenv("MOOX_JWT_ISSUER"); issuer != "" {
		return issuer
	}
	return "moox-server"
}

// GenerateToken 生成统一JWT令牌
func GenerateToken(claims *UnifiedClaims, secretKey string, expireDuration time.Duration) (string, error) {
	now := time.Now()
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(expireDuration))
	claims.NotBefore = jwt.NewNumericDate(now)
	claims.Issuer = DefaultJWTConfig.Issuer

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}

// GenerateAccessToken 生成API访问token
func GenerateAccessToken(userID, username string, role int32, secretKey string, expireDuration time.Duration) (string, error) {
	claims := &UnifiedClaims{
		UserID:    userID,
		Username:  username,
		Role:      role,
		TokenType: TokenTypeAccess,
	}
	return GenerateToken(claims, secretKey, expireDuration)
}

// GenerateFileDownloadToken 生成文件下载token
func GenerateFileDownloadToken(userID, filePath string, expireDuration time.Duration) (string, error) {
	claims := &UnifiedClaims{
		UserID:    userID,
		FilePath:  filePath,
		TokenType: TokenTypeFileDownload,
	}
	return GenerateToken(claims, DefaultJWTConfig.SecretKey, expireDuration)
}

// ParseToken 解析JWT令牌
func ParseToken(tokenString, secretKey string) (*UnifiedClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &UnifiedClaims{}, func(token *jwt.Token) (interface{}, error) {
		// 验证签名方法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		return nil, fmt.Errorf("token解析失败: %w", err)
	}

	claims, ok := token.Claims.(*UnifiedClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("无效的token")
	}

	return claims, nil
}

// ValidateAccessToken 验证API访问token
func ValidateAccessToken(tokenString, secretKey string) (*UnifiedClaims, error) {
	claims, err := ParseToken(tokenString, secretKey)
	if err != nil {
		return nil, err
	}

	// 验证token类型
	if claims.TokenType != TokenTypeAccess {
		return nil, fmt.Errorf("token类型错误: 期望 %s, 实际 %s", TokenTypeAccess, claims.TokenType)
	}

	return claims, nil
}

// ValidateFileDownloadToken 验证文件下载token
func ValidateFileDownloadToken(tokenString, expectedFilePath string) (*UnifiedClaims, error) {
	claims, err := ParseToken(tokenString, DefaultJWTConfig.SecretKey)
	if err != nil {
		return nil, err
	}

	// 验证token类型
	if claims.TokenType != TokenTypeFileDownload {
		return nil, fmt.Errorf("token类型错误: 期望 %s, 实际 %s", TokenTypeFileDownload, claims.TokenType)
	}

	// 验证文件路径是否匹配
	if claims.FilePath != expectedFilePath {
		return nil, fmt.Errorf("文件路径不匹配: 期望 %s, 实际 %s", expectedFilePath, claims.FilePath)
	}

	return claims, nil
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
