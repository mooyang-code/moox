// Package crypto 提供 Trade 模块的凭证加解密工具。
//
// 仅保留 AES-GCM 加解密，复用与 admin 一致的算法与密钥约定
// （security.encryption_key，32 字节对应 AES-256）。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// deriveKey 规范化密钥：长度为 16/24/32 时直接使用，否则用 sha256 派生 32 字节。
// 这样配置中的人类可读密钥（如 admin 默认的 29 字节串）也能安全使用。
func deriveKey(key string) []byte {
	k := []byte(key)
	switch len(k) {
	case 16, 24, 32:
		return k
	}
	sum := sha256.Sum256(k)
	return sum[:]
}

// AESEncrypt 使用 AES-GCM 加密，返回 Base64 编码密文（nonce 前置）。
// key 长度需为 16/24/32 字节。
func AESEncrypt(plaintext, key string) (string, error) {
	if plaintext == "" {
		return "", errors.New("plaintext cannot be empty")
	}
	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// AESDecrypt 使用 AES-GCM 解密 Base64 编码密文。
func AESDecrypt(ciphertext, key string) (string, error) {
	if ciphertext == "" {
		return "", errors.New("ciphertext cannot be empty")
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// MaskAPIKey 脱敏：仅保留前 4 与后 4 字符，中间用 **** 代替。
// 短键直接全部 ****。
func MaskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
