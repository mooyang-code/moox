package fileserver

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mooyang-code/moox/server/internal/service/auth/utils"
)

// TokenType 文件下载token类型
const TokenTypeFileDownload = "file_download"

// getJWTSecretKey 获取JWT密钥
func getJWTSecretKey() string {
	// 优先从环境变量读取
	if secretKey := os.Getenv("MOOX_JWT_SECRET_KEY"); secretKey != "" {
		return secretKey
	}

	// 兜底：使用默认值（仅用于开发环境）
	return "kJ8#3Lz!b1A6xQwP2dR9vM4nS7eT0uYpG5hZcV8jF2mB6sXlD3rWqN0tH9uK1oE4"
}

// GenerateFileDownloadToken 生成文件下载token
func GenerateFileDownloadToken(userID, filePath string, expireDuration time.Duration) (string, error) {
	claims := &utils.UnifiedClaims{
		UserID:    userID,
		FilePath:  filePath,
		TokenType: utils.TokenType(TokenTypeFileDownload),
	}
	return generateToken(claims, getJWTSecretKey(), expireDuration)
}

// ValidateFileDownloadToken 验证文件下载token
func ValidateFileDownloadToken(tokenString, expectedFilePath string) (*utils.UnifiedClaims, error) {
	claims, err := parseToken(tokenString, getJWTSecretKey())
	if err != nil {
		return nil, err
	}

	// 验证token类型
	if claims.TokenType != utils.TokenType(TokenTypeFileDownload) {
		return nil, fmt.Errorf("token类型错误: 期望 %s, 实际 %s", TokenTypeFileDownload, claims.TokenType)
	}

	// 验证文件路径是否匹配
	if claims.FilePath != expectedFilePath {
		return nil, fmt.Errorf("文件路径不匹配: 期望 %s, 实际 %s", expectedFilePath, claims.FilePath)
	}

	return claims, nil
}

// generateToken 生成JWT令牌
func generateToken(claims *utils.UnifiedClaims, secretKey string, expireDuration time.Duration) (string, error) {
	now := time.Now()
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(expireDuration))
	claims.NotBefore = jwt.NewNumericDate(now)
	claims.Issuer = utils.DefaultJWTConfig.Issuer

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}

// parseToken 解析JWT令牌
func parseToken(tokenString, secretKey string) (*utils.UnifiedClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &utils.UnifiedClaims{}, func(token *jwt.Token) (interface{}, error) {
		// 验证签名方法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		return nil, fmt.Errorf("token解析失败: %w", err)
	}

	claims, ok := token.Claims.(*utils.UnifiedClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("无效的token")
	}

	return claims, nil
}
