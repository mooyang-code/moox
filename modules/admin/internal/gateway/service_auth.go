package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultServiceAuthVersion       = "moox-auth-v1"
	defaultServiceAuthExpireSeconds = int64(1800)
	defaultServiceAuthClockSkewSecs = int64(300)
)

func normalizeServiceAuthConfig(cfg ServiceAuthConfig) ServiceAuthConfig {
	if cfg.Version == "" {
		cfg.Version = defaultServiceAuthVersion
	}
	if cfg.AccessKey == "" {
		cfg.AccessKey = os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY")
	}
	if cfg.SecretKey == "" {
		cfg.SecretKey = os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY")
	}
	if cfg.MaxExpireSecs <= 0 {
		cfg.MaxExpireSecs = defaultServiceAuthExpireSeconds
	}
	if cfg.ClockSkewSecs <= 0 {
		cfg.ClockSkewSecs = defaultServiceAuthClockSkewSecs
	}
	return cfg
}

func currentServiceAuthConfig() (ServiceAuthConfig, error) {
	cfg := ServiceAuthConfig{}
	if loaded := GetConfig(); loaded != nil {
		cfg = loaded.Gateway.ServiceAuth
	}
	cfg = normalizeServiceAuthConfig(cfg)
	if !cfg.Enabled {
		return cfg, fmt.Errorf("service auth is disabled")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return cfg, fmt.Errorf("service auth access_key and secret_key are required")
	}
	return cfg, nil
}

func validateServiceAuthHeader(authHeader string, body []byte, now time.Time, cfg ServiceAuthConfig) error {
	cfg = normalizeServiceAuthConfig(cfg)
	parts := strings.Split(authHeader, "/")
	if len(parts) != 5 {
		return fmt.Errorf("invalid auth format")
	}
	version, accessKey, timestampText, expireText, signature := parts[0], parts[1], parts[2], parts[3], parts[4]
	if version != cfg.Version {
		return fmt.Errorf("invalid auth version")
	}
	if accessKey != cfg.AccessKey {
		return fmt.Errorf("invalid access key")
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp")
	}
	expireSeconds, err := strconv.ParseInt(expireText, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expire time")
	}
	if expireSeconds <= 0 || expireSeconds > cfg.MaxExpireSecs {
		return fmt.Errorf("invalid expire time")
	}

	nowUnix := now.Unix()
	if nowUnix+cfg.ClockSkewSecs < timestamp {
		return fmt.Errorf("auth timestamp is in the future")
	}
	if nowUnix > timestamp+expireSeconds+cfg.ClockSkewSecs {
		return fmt.Errorf("auth signature expired")
	}

	prefix := fmt.Sprintf("%s/%s/%d/%d", version, accessKey, timestamp, expireSeconds)
	expected := generateServiceAuthSignature(cfg.SecretKey, prefix, string(body))
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func generateServiceAuthSignature(secretKey string, prefix string, body string) string {
	signKeyHex := hmacSha256Hex(secretKey, prefix)
	return hmacSha256Hex(signKeyHex, body)
}

func hmacSha256Hex(key string, data string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func GenerateServiceAuthHeaderForTest(version string, accessKey string, secretKey string, body string, timestamp int64, expireSeconds int64) string {
	prefix := fmt.Sprintf("%s/%s/%d/%d", version, accessKey, timestamp, expireSeconds)
	return fmt.Sprintf("%s/%s", prefix, generateServiceAuthSignature(secretKey, prefix, body))
}
