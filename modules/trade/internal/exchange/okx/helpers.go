package okx

import (
	"crypto/hmac"
	"crypto/sha256"
)

// hmacSha256 返回 HMAC-SHA256 摘要。
func hmacSha256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
