package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-go/log"
)

// AESEncrypt 使用 AES-GCM 模式加密数据
// plaintext: 明文
// key: 密钥（必须是 16、24 或 32 字节，分别对应 AES-128、AES-192 或 AES-256）
// 返回: Base64 编码的密文
func AESEncrypt(plaintext, key string) (string, error) {
	if plaintext == "" {
		return "", errors.New("plaintext cannot be empty")
	}

	// 创建 AES 加密块
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}

	// 使用 GCM 模式（推荐的认证加密模式）
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// 生成随机 nonce（每次加密都应该使用不同的 nonce）
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// 加密数据（GCM 模式会自动添加认证标签）
	// nonce 会被添加到密文前面，以便解密时使用
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// 返回 Base64 编码的密文
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// AESDecrypt 使用 AES-GCM 模式解密数据
// ciphertext: Base64 编码的密文
// key: 密钥（必须与加密时使用的密钥相同）
// 返回: 明文
func AESDecrypt(ciphertext, key string) (string, error) {
	if ciphertext == "" {
		return "", errors.New("ciphertext cannot be empty")
	}

	// Base64 解码
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	// 创建 AES 加密块
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}

	// 使用 GCM 模式
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// 检查密文长度
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	// 提取 nonce 和实际密文
	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]

	// 解密数据（GCM 模式会自动验证认证标签）
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// ========== 密码哈希相关 ==========

// GenerateSalt 生成随机盐值
func GenerateSalt() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// GenerateUserID 生成用户ID
func GenerateUserID() string {
	return uuid.New().String()
}

// HashPassword 哈希密码
func HashPassword(password, salt string) string {
	hash := sha256.Sum256([]byte(password + salt))
	return hex.EncodeToString(hash[:])
}

// VerifyPasswordHash 验证密码哈希
func VerifyPasswordHash(password, salt, hash string) bool {
	return HashPassword(password, salt) == hash
}

// ValidateHash 验证客户端提交的哈希值
func ValidateHash(password, salt string, timestamp int64, providedHash string) bool {
	expectedHash := sha256.Sum256([]byte(password + salt + fmt.Sprint(timestamp)))
	expectedHashStr := hex.EncodeToString(expectedHash[:])
	return expectedHashStr == providedHash
}

// DeriveEncryptionKey 从盐值和时间戳派生加密密钥
func DeriveEncryptionKey(salt string, timestamp int64) []byte {
	// 使用盐值和时间戳生成32字节的密钥
	keyMaterial := salt + fmt.Sprint(timestamp)
	hash := sha256.Sum256([]byte(keyMaterial))
	return hash[:]
}

// AESDecryptWithKey AES-GCM解密（接受字节密钥）
func AESDecryptWithKey(ciphertext string, key []byte) (string, error) {
	// Base64解码
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext_bytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext_bytes, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// ValidateEncryptedPassword 验证客户端提交的加密密码
func ValidateEncryptedPassword(ctx context.Context, storedPasswordHash, userStoredSalt, dynamicSalt string,
	timestamp int64, encryptedPassword string) bool {
	// 1. 使用动态盐值和时间戳派生解密密钥
	key := DeriveEncryptionKey(dynamicSalt, timestamp)

	// 2. 解密得到原始密码
	password, err := AESDecryptWithKey(encryptedPassword, key)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 密码解密失败: %v", err)
		return false
	}
	log.InfoContextf(ctx, "[Auth] 解密得到的密码: %s", password)

	// 3. 使用用户存储的盐值验证密码哈希
	return VerifyPasswordHash(password, userStoredSalt, storedPasswordHash)
}

// DecryptPassword 解密客户端发送的密码
func DecryptPassword(encryptedPassword, salt string, timestamp int64) (string, error) {
	// 派生解密密钥
	key := DeriveEncryptionKey(salt, timestamp)

	// 解密得到原始密码
	return AESDecryptWithKey(encryptedPassword, key)
}

// ========== JWT相关 ==========

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

// getJWTSecretKey 获取JWT密钥
func getJWTSecretKey() string {
	// 优先从环境变量读取
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
	return "moox-admin"
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
