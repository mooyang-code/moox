package util

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

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

// AESEncrypt AES-GCM加密
func AESEncrypt(plaintext string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// 生成随机nonce
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	// 加密
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// AESDecrypt AES-GCM解密
func AESDecrypt(ciphertext string, key []byte) (string, error) {
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
func ValidateEncryptedPassword(storedPasswordHash, salt string, timestamp int64, encryptedPassword string) bool {
	// 派生解密密钥
	key := DeriveEncryptionKey(salt, timestamp)

	// 解密得到原始密码
	password, err := AESDecrypt(encryptedPassword, key)
	if err != nil {
		return false
	}

	// 验证密码哈希
	return VerifyPasswordHash(password, salt, storedPasswordHash)
}

// DecryptPassword 解密客户端发送的密码
func DecryptPassword(encryptedPassword, salt string, timestamp int64) (string, error) {
	// 派生解密密钥
	key := DeriveEncryptionKey(salt, timestamp)

	// 解密得到原始密码
	return AESDecrypt(encryptedPassword, key)
}

// JWTClaims JWT声明
type JWTClaims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     int32  `json:"role"`
	jwt.RegisteredClaims
}

// GenerateJWT 生成JWT令牌
func GenerateJWT(userID, username string, role int32, secretKey string, expiredDuration time.Duration) (string, error) {
	claims := JWTClaims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiredDuration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}

// ParseJWT 解析JWT令牌
func ParseJWT(tokenString, secretKey string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}
