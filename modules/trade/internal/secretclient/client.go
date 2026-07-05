// Package secretclient 从 admin 网关读取后台秘钥管理中的交易所凭证。
package secretclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
)

// Config 是 SecretMgr 后台服务调用配置。
type Config struct {
	GatewayBaseURL string
	ServiceAuth    ServiceAuthConfig
	Timeout        time.Duration
}

// ServiceAuthConfig 与 admin gateway service_auth 配置保持一致。
type ServiceAuthConfig struct {
	Version    string
	AccessKey  string
	SecretKey  string
	ExpireSecs int64
}

// Client 调用 admin SecretMgr。
type Client struct {
	baseURL string
	auth    ServiceAuthConfig
	client  *http.Client
}

// New 创建 SecretMgr client。
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.GatewayBaseURL, "/"),
		auth:    cfg.ServiceAuth,
		// 目标网关来自本服务可信配置，不接受用户输入；这里使用固定超时 HTTP client。
		client: &http.Client{Timeout: timeout},
	}
}

// ListExchangeSecrets 返回指定交易所的 active exchange secret，并逐条 reveal 明文 secret_value。
func (c *Client) ListExchangeSecrets(ctx context.Context, provider string) ([]service.ExchangeSecret, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "binance"
	}
	var list listSecretsRsp
	if err := c.post(ctx, "ListSecrets", listSecretsReq{
		Category: "exchange",
		Provider: provider,
		Status:   "active",
		Offset:   0,
		Limit:    200,
	}, &list); err != nil {
		return nil, err
	}
	out := make([]service.ExchangeSecret, 0, len(list.Secrets))
	for _, sec := range list.Secrets {
		if sec.SecretID == "" {
			continue
		}
		var reveal revealSecretRsp
		if err := c.post(ctx, "RevealSecret", revealSecretReq{SecretID: sec.SecretID}, &reveal); err != nil {
			return nil, err
		}
		plain := reveal.Secret
		if plain.SecretID == "" {
			plain = sec
		}
		out = append(out, service.ExchangeSecret{
			SecretID:    plain.SecretID,
			Name:        plain.Name,
			Description: plain.Description,
			Provider:    plain.Provider,
			KeyID:       plain.KeyID,
			SecretValue: plain.SecretValue,
			ExtraConfig: plain.ExtraConfig,
		})
	}
	return out, nil
}

func (c *Client) post(ctx context.Context, method string, req any, rsp responseWithRetInfo) error {
	if c.baseURL == "" {
		return fmt.Errorf("secret client gateway base url is empty")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/service/secret/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	auth, err := c.authHeader(body, time.Now())
	if err != nil {
		return err
	}
	httpReq.Header.Set("Auth", auth)
	httpRsp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpRsp.Body.Close()
	if httpRsp.StatusCode < 200 || httpRsp.StatusCode >= 300 {
		return fmt.Errorf("secret %s HTTP %d", method, httpRsp.StatusCode)
	}
	if err := json.NewDecoder(httpRsp.Body).Decode(rsp); err != nil {
		return err
	}
	if !rsp.retOK() {
		return fmt.Errorf("secret %s failed: %s", method, rsp.retMessage())
	}
	return nil
}

func (c ServiceAuthConfig) normalized() ServiceAuthConfig {
	if c.Version == "" {
		c.Version = "moox-auth-v1"
	}
	if c.ExpireSecs <= 0 {
		c.ExpireSecs = 1800
	}
	return c
}

func (c *Client) authHeader(body []byte, now time.Time) (string, error) {
	auth := c.auth.normalized()
	if auth.AccessKey == "" || auth.SecretKey == "" {
		return "", fmt.Errorf("service auth access_key and secret_key are required")
	}
	prefix := fmt.Sprintf("%s/%s/%d/%d", auth.Version, auth.AccessKey, now.Unix(), auth.ExpireSecs)
	signKeyHex := hmacSHA256Hex(auth.SecretKey, prefix)
	signature := hmacSHA256Hex(signKeyHex, string(body))
	return fmt.Sprintf("%s/%s", prefix, signature), nil
}

func hmacSHA256Hex(key, data string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

type responseWithRetInfo interface {
	retOK() bool
	retMessage() string
}

type retInfo struct {
	Code any    `json:"code"`
	Msg  string `json:"msg"`
}

func (r retInfo) ok() bool {
	switch v := r.Code.(type) {
	case float64:
		return v == 0
	case int:
		return v == 0
	case string:
		return v == "0" || strings.EqualFold(v, "SUCCESS")
	default:
		return false
	}
}

type secretDTO struct {
	SecretID    string `json:"secret_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Provider    string `json:"provider"`
	SecretType  string `json:"secret_type"`
	KeyID       string `json:"key_id"`
	SecretValue string `json:"secret_value"`
	ExtraConfig string `json:"extra_config"`
	Status      string `json:"status"`
}

type listSecretsReq struct {
	Category string `json:"category"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Offset   int32  `json:"offset"`
	Limit    int32  `json:"limit"`
}

type listSecretsRsp struct {
	RetInfo retInfo     `json:"ret_info"`
	Secrets []secretDTO `json:"secrets"`
}

func (r *listSecretsRsp) retOK() bool        { return r.RetInfo.ok() }
func (r *listSecretsRsp) retMessage() string { return r.RetInfo.Msg }

type revealSecretReq struct {
	SecretID string `json:"secret_id"`
}

type revealSecretRsp struct {
	RetInfo retInfo   `json:"ret_info"`
	Secret  secretDTO `json:"secret"`
}

func (r *revealSecretRsp) retOK() bool        { return r.RetInfo.ok() }
func (r *revealSecretRsp) retMessage() string { return r.RetInfo.Msg }
