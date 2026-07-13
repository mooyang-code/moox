package gateway

import (
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/packages/serviceauth"
)

const (
	defaultServiceAuthVersion       = serviceauth.Version
	defaultServiceAuthExpireSeconds = int64(60)
	defaultServiceAuthClockSkewSecs = int64(30)
)

var serviceNonceCache = serviceauth.NewNonceCache(65536)

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
	if cfg.Version != serviceauth.Version {
		return cfg, fmt.Errorf("service auth version must be %s", serviceauth.Version)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return cfg, fmt.Errorf("service auth access_key and secret_key are required")
	}
	return cfg, nil
}

func validateServiceAuthHeader(header, method, path string, body []byte, now time.Time, cfg ServiceAuthConfig) error {
	cfg = normalizeServiceAuthConfig(cfg)
	claims, err := serviceauth.VerifyHeader(serviceauth.Config{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, ExpireSeconds: cfg.MaxExpireSecs, ClockSkewSeconds: cfg.ClockSkewSecs}, serviceauth.Request{Method: method, Path: path, Body: body}, header, now)
	if err != nil {
		return err
	}
	if !serviceNonceCache.Consume(claims.AccessKey, claims.Nonce, claims.TTL, now) {
		return fmt.Errorf("service auth nonce was already used")
	}
	return nil
}
