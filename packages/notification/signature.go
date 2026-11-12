package notification

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// WebhookSignature 返回出站 Webhook 签名头的值：sha256=<HMAC-SHA256(密钥, 时间戳 + "." + 请求体) 的十六进制>。
// 事件中心的回调与告警的通用 Webhook 共用这一个约定：请求头 X-MooX-Timestamp 是毫秒时间戳，X-MooX-Signature 是本函数的结果。
func WebhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
