// Package serviceauth 生成 Storage 进程之间互相调用用的服务签名。
package serviceauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Key 返回调用方 appID 在密钥 secret 下的签名（十六进制 HMAC-SHA256）。
func Key(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}

// Valid 判断 appKey 是否是 appID 在密钥 secret 下的签名，大小写不敏感。
func Valid(secret, appID, appKey string) bool {
	return hmac.Equal([]byte(strings.ToLower(appKey)), []byte(Key(secret, appID)))
}
