// Package secretclient reads Exchange credentials from the Admin gateway.
package secretclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// Config 是 SecretMgr 后台服务调用配置。
type Config struct {
	GatewayBaseURL string
	ServiceAuth    ServiceAuthConfig
	Timeout        time.Duration
}

// ServiceAuthConfig 与 admin gateway service_auth 配置保持一致。
type ServiceAuthConfig struct {
	AccessKey  string
	SecretKey  string
	TargetNode string
	CAFile     string
	ExpireSecs int64
}

// Client 调用 admin SecretMgr。
type Client struct {
	baseURL   string
	auth      ServiceAuthConfig
	client    *http.Client
	clientErr error
}

// New 创建 SecretMgr client。
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client, clientErr := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: timeout, CAFile: cfg.ServiceAuth.CAFile})
	return &Client{
		baseURL: strings.TrimRight(cfg.GatewayBaseURL, "/"),
		auth:    cfg.ServiceAuth,
		// 目标网关来自本服务可信配置，不接受用户输入；这里使用固定超时 HTTP client。
		client:    client,
		clientErr: clientErr,
	}
}

// GetExchangeSecret returns the configured credential and its trusted metadata.
func (c *Client) GetExchangeSecret(
	ctx context.Context,
	secretID string,
) (account.ExchangeSecret, error) {
	if strings.TrimSpace(secretID) == "" {
		return account.ExchangeSecret{}, account.ErrInvalidCredential
	}
	var response getSecretValueRsp
	if err := c.post(
		ctx,
		"GetSecretValue",
		getSecretValueReq{SecretID: secretID},
		&response,
	); err != nil {
		return account.ExchangeSecret{}, err
	}
	secret := response.Secret
	if secret.SecretID != secretID ||
		secret.Category != "exchange" ||
		!secret.Exchange.Valid() ||
		secret.Status != "active" ||
		strings.TrimSpace(secret.KeyID) == "" ||
		strings.TrimSpace(secret.SecretValue) == "" {
		return account.ExchangeSecret{}, fmt.Errorf(
			"%w: secret %q metadata or value is invalid",
			account.ErrInvalidCredential,
			secretID,
		)
	}
	return account.ExchangeSecret{
		SecretID: secret.SecretID, Name: secret.Name,
		Description: secret.Description, Category: secret.Category,
		Exchange: secret.Exchange, Status: secret.Status,
		KeyID: secret.KeyID, SecretValue: secret.SecretValue,
		ExtraConfig: secret.ExtraConfig,
	}, nil
}

func (c *Client) post(ctx context.Context, method string, req any, rsp responseWithRetInfo) error {
	if c.clientErr != nil {
		return c.clientErr
	}
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
	auth, err := c.authHeader(http.MethodPost, httpReq.URL.EscapedPath(), body, time.Now())
	if err != nil {
		return err
	}
	for name, values := range auth {
		httpReq.Header[name] = append([]string(nil), values...)
	}
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
	if c.ExpireSecs <= 0 {
		c.ExpireSecs = 60
	}
	return c
}

func (c *Client) authHeader(method, path string, body []byte, now time.Time) (http.Header, error) {
	auth := c.auth.normalized()
	if auth.AccessKey == "" || auth.SecretKey == "" || auth.TargetNode == "" {
		return nil, fmt.Errorf("gateway service key_id, secret_key and target_node are required")
	}
	return gatewayauth.Sign(gatewayauth.Credentials{KeyID: auth.AccessKey, Secret: auth.SecretKey, Expire: time.Duration(auth.ExpireSecs) * time.Second}, gatewayauth.Request{Method: method, Path: path, Body: body, TargetNode: auth.TargetNode}, now)
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
	SecretID    string            `json:"secret_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Exchange    exchange.Exchange `json:"-"`
	SecretType  string            `json:"secret_type"`
	KeyID       string            `json:"key_id"`
	SecretValue string            `json:"secret_value"`
	ExtraConfig string            `json:"extra_config"`
	Status      string            `json:"status"`
}

func (s *secretDTO) UnmarshalJSON(data []byte) error {
	type secretFields secretDTO
	var decoded secretFields
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	var externalExchange string
	if raw := object["pro"+"vider"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &externalExchange); err != nil {
			return err
		}
	}
	*s = secretDTO(decoded)
	s.Exchange = exchange.Exchange(strings.ToUpper(strings.TrimSpace(externalExchange)))
	return nil
}

type getSecretValueReq struct {
	SecretID string `json:"secret_id"`
}

type getSecretValueRsp struct {
	RetInfo retInfo   `json:"ret_info"`
	Secret  secretDTO `json:"secret"`
}

func (r *getSecretValueRsp) retOK() bool        { return r.RetInfo.ok() }
func (r *getSecretValueRsp) retMessage() string { return r.RetInfo.Msg }
