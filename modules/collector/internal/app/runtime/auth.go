package runtime

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	trpc "trpc.group/trpc-go/trpc-go"
)

const defaultExpireSec = int64(60)

// AuthConfig describes the HMAC authentication used by backend service APIs.
type AuthConfig struct {
	AccessKey   string
	SecretKey   string
	TargetNode  string
	CAFile      string
	CAPEMBase64 string
	NowUnix     int64
	ExpireSec   int64
}

func DefaultAuthConfig() AuthConfig {
	cfg := GetServiceAuthConfig()
	return AuthConfig{
		AccessKey:   cfg.AccessKey,
		SecretKey:   cfg.SecretKey,
		TargetNode:  cfg.TargetNode,
		CAFile:      cfg.CAFile,
		CAPEMBase64: cfg.CAPEMBase64,
		ExpireSec:   cfg.ExpireSec,
	}
}

func NewGatewayHTTPClient(timeout time.Duration, cfg AuthConfig) (*http.Client, error) {
	return gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: timeout, CAFile: cfg.CAFile, CAPEMBase64: cfg.CAPEMBase64})
}

func normalizeAuthConfig(cfg AuthConfig) AuthConfig {
	if cfg.NowUnix <= 0 {
		cfg.NowUnix = time.Now().Unix()
	}
	if cfg.ExpireSec <= 0 {
		cfg.ExpireSec = defaultExpireSec
	}
	return cfg
}

func GenerateAuthHeader(cfg AuthConfig, method, path string, body []byte) (http.Header, error) {
	cfg = normalizeAuthConfig(cfg)
	return gatewayauth.Sign(gatewayauth.Credentials{KeyID: cfg.AccessKey, Secret: cfg.SecretKey, Expire: time.Duration(cfg.ExpireSec) * time.Second}, gatewayauth.Request{Method: method, Path: path, Body: body, TargetNode: cfg.TargetNode}, time.Unix(cfg.NowUnix, 0))
}

func NewSignedRequest(method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	return NewSignedRequestWithContext(trpc.BackgroundContext(), method, url, body, cfg)
}

func NewSignedRequestWithContext(ctx context.Context, method string, url string, body []byte, cfg AuthConfig) (*http.Request, error) {
	return NewSignedRequestWithContextAndHeaders(ctx, method, url, body, nil, cfg)
}

func NewSignedRequestWithContextAndHeaders(ctx context.Context, method string, url string, body []byte, headers map[string]string, cfg AuthConfig) (*http.Request, error) {
	cfg = normalizeAuthConfig(cfg)
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.TargetNode == "" {
		return nil, fmt.Errorf("gateway service key_id, secret_key and target_node are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	header, err := GenerateAuthHeader(cfg, method, path, body)
	if err != nil {
		return nil, err
	}
	copyGatewayHeaders(req.Header, header)
	return req, nil
}

func copyGatewayHeaders(dst, src http.Header) {
	for name, values := range src {
		dst[name] = append([]string(nil), values...)
	}
}
