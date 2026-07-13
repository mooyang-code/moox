package cloudruntime

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mooyang-code/moox/packages/serviceauth"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultAuthVersion = serviceauth.Version
	defaultExpireSec   = int64(60)
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
	header, err := generateAuthHeader(cfg, method, req.URL.EscapedPath(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Auth", header)
	return req, nil
}

func generateAuthHeader(cfg AuthConfig, method, path string, body []byte) (string, error) {
	cfg = normalizeAuthConfig(cfg)
	if _, err := url.Parse(path); err != nil {
		return "", err
	}
	return serviceauth.BuildHeader(serviceauth.Config{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, ExpireSeconds: cfg.ExpireSec}, serviceauth.Request{Method: method, Path: path, Body: body}, time.Unix(cfg.NowUnix, 0))
}
