package crypto

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/mooyang-code/moox/server/internal/config"
)

// GetEncryptionKey 获取加密密钥
// 从全局配置中读取，配置优先级：环境变量 > 配置文件 > 默认值
func GetEncryptionKey() string {
	key := config.GetEncryptionKey()

	// 确保密钥长度为32字节（AES-256）
	return ensureKeyLength(key, 32)
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
