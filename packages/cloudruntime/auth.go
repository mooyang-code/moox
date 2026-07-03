package cloudruntime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultAuthVersion = "moox-auth-v1"
	defaultExpireSec   = int64(1800)
)

// AuthConfig describes the HMAC authentication used by MooX backend service APIs.
type AuthConfig struct {
	Version   string
	AccessKey string
	SecretKey string
	NowUnix   int64
	ExpireSec int64
}

func normalizeAuthConfig(cfg AuthConfig) AuthConfig {
	if cfg.Version == "" {
		cfg.Version = defaultAuthVersion
	}
	if cfg.NowUnix <= 0 {
		cfg.NowUnix = time.Now().Unix()
	}
	if cfg.ExpireSec <= 0 {
		cfg.ExpireSec = defaultExpireSec
	}
	return cfg
}

func newSignedRequestWithContext(ctx context.Context, method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	cfg = normalizeAuthConfig(cfg)
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("cloud runtime service auth access_key and secret_key are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Auth", generateAuthHeader(cfg, string(body)))
	return req, nil
}

func generateAuthHeader(cfg AuthConfig, body string) string {
	cfg = normalizeAuthConfig(cfg)
	prefix := fmt.Sprintf("%s/%s/%d/%d", cfg.Version, cfg.AccessKey, cfg.NowUnix, cfg.ExpireSec)
	signKeyHex := hmacSha256Hex(cfg.SecretKey, prefix)
	signature := hmacSha256Hex(signKeyHex, body)
	return fmt.Sprintf("%s/%s", prefix, signature)
}

func hmacSha256Hex(key string, data string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
