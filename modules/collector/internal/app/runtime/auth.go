package runtime

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mooyang-code/moox/packages/servicegateway"
	"net/http"
	"time"
)

const (
	defaultAuthVersion = servicegateway.Version
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
	return servicegateway.BuildHeader(servicegateway.AuthConfig{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, ExpireSeconds: cfg.ExpireSec}, servicegateway.AuthRequest{Method: method, Path: path, Body: body}, time.Unix(cfg.NowUnix, 0))
}

func NewSignedRequest(method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	return NewSignedRequestWithContext(context.Background(), method, url, body, cfg)
}

func NewSignedRequestWithContext(ctx context.Context, method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	return NewSignedRequestWithContextAndHeaders(ctx, method, url, body, nil, cfg)
}

func NewSignedRequestWithContextAndHeaders(ctx context.Context, method string, url string, body []byte, headers map[string]string, cfg AuthConfig) (*http.Request, error) {
	cfg = normalizeAuthConfig(cfg)
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("control service auth access_key and secret_key are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	header, err := servicegateway.BuildHeader(
		servicegateway.AuthConfig{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, ExpireSeconds: cfg.ExpireSec},
		servicegateway.AuthRequest{Method: method, Path: req.URL.EscapedPath(), Body: body, Headers: headers},
		time.Unix(cfg.NowUnix, 0),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Auth", header)
	return req, nil
}
