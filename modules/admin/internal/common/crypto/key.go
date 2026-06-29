package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// GetEncryptionKey 获取加密密钥
// 优先级：环境变量 MOOX_ENCRYPTION_KEY > 默认开发密钥
// 生产环境务必设置 MOOX_ENCRYPTION_KEY 环境变量。
func GetEncryptionKey() string {
	if key := os.Getenv("MOOX_ENCRYPTION_KEY"); key != "" {
		return ensureKeyLength(key, 32)
	}
	return ensureKeyLength("moox-cloud-secret-key-32bytes", 32)
}

// ensureKeyLength 确保密钥长度符合要求
// 如果长度不够，使用 SHA-256 哈希扩展
// 如果长度过长，截取前 n 字节
func ensureKeyLength(key string, length int) string {
	if len(key) == length {
		return key
	}

	if len(key) > length {
		return key[:length]
	}

	// 使用 SHA-256 哈希来生成固定长度的密钥
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])[:length]
}
