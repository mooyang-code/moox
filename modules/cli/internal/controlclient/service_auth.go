package controlclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// ServiceAuthConfig 后台服务签名鉴权配置
// 与 control 端 gateway/service_auth.go 的算法保持一致：
//
//	Auth header = "<version>/<access_key>/<timestamp>/<expire_seconds>/<signature>"
//	signature  = HMAC_SHA256_HEX( HMAC_SHA256_HEX(secret_key, prefix), body )
//	prefix     = "<version>/<access_key>/<timestamp>/<expire_seconds>"
type ServiceAuthConfig struct {
	Version      string
	AccessKey    string
	SecretKey    string
	ExpireSecs   int64
}

// BuildAuthHeader 生成后台服务鉴权 Auth 头
func (c ServiceAuthConfig) BuildAuthHeader(body []byte, now time.Time) (string, error) {
	if c.AccessKey == "" || c.SecretKey == "" {
		return "", fmt.Errorf("service auth access_key and secret_key are required")
	}
	version := c.Version
	if version == "" {
		version = "moox-auth-v1"
	}
	expire := c.ExpireSecs
	if expire <= 0 {
		expire = 1800
	}
	ts := now.Unix()
	prefix := fmt.Sprintf("%s/%s/%d/%d", version, c.AccessKey, ts, expire)
	signKeyHex := hmacSha256Hex(c.SecretKey, prefix)
	signature := hmacSha256Hex(signKeyHex, string(body))
	return fmt.Sprintf("%s/%s", prefix, signature), nil
}

func hmacSha256Hex(key string, data string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
