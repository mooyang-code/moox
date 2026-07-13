package runtime

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mooyang-code/moox/packages/serviceauth"
	"net/http"
	"time"
)

const (
	defaultAuthVersion = serviceauth.Version
	defaultExpireSec   = int64(60)
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
	cfg := GetServiceAuthConfig()
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

func GenerateAuthHeader(cfg AuthConfig, method, path string, body []byte) (string, error) {
	cfg = normalizeAuthConfig(cfg)
	return serviceauth.BuildHeader(serviceauth.Config{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, ExpireSeconds: cfg.ExpireSec}, serviceauth.Request{Method: method, Path: path, Body: body}, time.Unix(cfg.NowUnix, 0))
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
	header, err := GenerateAuthHeader(cfg, method, req.URL.EscapedPath(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Auth", header)
	return req, nil
}
