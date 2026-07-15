package cloudruntime

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

const defaultExpireSec = int64(60)

// AuthConfig describes the HMAC authentication used by MooX backend service APIs.
type AuthConfig struct {
	AccessKey  string
	SecretKey  string
	TargetNode string
	CAFile     string
	NowUnix    int64
	ExpireSec  int64
}

func normalizeAuthConfig(cfg AuthConfig) AuthConfig {
	if cfg.TargetNode == "" {
		cfg.TargetNode = os.Getenv("MOOX_GATEWAY_NODE_ID")
	}
	if cfg.AccessKey == "" {
		cfg.AccessKey = os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID")
	}
	if cfg.SecretKey == "" {
		cfg.SecretKey = os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY")
	}
	if cfg.CAFile == "" {
		cfg.CAFile = os.Getenv("MOOX_GATEWAY_CA_FILE")
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
	return newSignedRequestWithNormalizedAuth(ctx, method, url, body, cfg)
}

func newSignedRequestWithNormalizedAuth(ctx context.Context, method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.TargetNode == "" {
		return nil, fmt.Errorf("cloud runtime gateway key_id, secret_key and target_node are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	header, err := signAuthHeader(cfg, method, req.URL.EscapedPath(), body)
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		req.Header[name] = append([]string(nil), values...)
	}
	return req, nil
}

func generateAuthHeader(cfg AuthConfig, method, path string, body []byte) (http.Header, error) {
	cfg = normalizeAuthConfig(cfg)
	return signAuthHeader(cfg, method, path, body)
}

func signAuthHeader(cfg AuthConfig, method, path string, body []byte) (http.Header, error) {
	return gatewayauth.Sign(gatewayauth.Credentials{KeyID: cfg.AccessKey, Secret: cfg.SecretKey, Expire: time.Duration(cfg.ExpireSec) * time.Second}, gatewayauth.Request{Method: method, Path: path, Body: body, TargetNode: cfg.TargetNode}, time.Unix(cfg.NowUnix, 0))
}
