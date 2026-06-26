package controlapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/config"
)

const (
	defaultAuthVersion = "moox-auth-v1"
	defaultExpireSec   = int64(1800)
)

// AuthConfig describes the HMAC authentication used by backend service APIs.
type AuthConfig struct {
	Version   string
	AccessKey string
	SecretKey string
	NowUnix   int64
	ExpireSec int64
}

func DefaultAuthConfig() AuthConfig {
	cfg := config.GetServiceAuthConfig()
	return AuthConfig{
		Version:   cfg.Version,
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
		ExpireSec: cfg.ExpireSec,
	}
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

// GenerateAuthHeader returns moox-auth-v1/$access_key/$timestamp/$expiretime/$signature.
func GenerateAuthHeader(cfg AuthConfig, body string) string {
	cfg = normalizeAuthConfig(cfg)
	prefix := fmt.Sprintf("%s/%s/%d/%d", cfg.Version, cfg.AccessKey, cfg.NowUnix, cfg.ExpireSec)
	signKeyHex := hmacSha256Hex(cfg.SecretKey, prefix)
	signature := hmacSha256Hex(signKeyHex, body)
	return fmt.Sprintf("%s/%s", prefix, signature)
}

func NewSignedRequest(method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	return NewSignedRequestWithContext(context.Background(), method, url, body, cfg)
}

func NewSignedRequestWithContext(ctx context.Context, method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	cfg = normalizeAuthConfig(cfg)
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("control service auth access_key and secret_key are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Auth", GenerateAuthHeader(cfg, string(body)))
	return req, nil
}

func hmacSha256Hex(key string, data string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
