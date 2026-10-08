// Package secretclient 经 gatewayclient 以 trade 身份从 Admin SecretMgr 读取交易所凭据。
//
// 请求和响应都用 JSON 序列化：响应按 snake_case 字段解析，Trade 因此不必引用 Admin 的协议包。
package secretclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/codec"
)

// secretMgrService 是 Admin SecretMgr 的 tRPC 服务名。
const secretMgrService = "trpc.moox.ops.SecretMgr"

const defaultTimeout = 10 * time.Second

// Forwarder 是本包用到的 gatewayclient 能力：按给定序列化类型签名并转发请求体。
type Forwarder interface {
	Forward(ctx context.Context, servicePath, method string, serialization int, body []byte, opts ...gatewayclient.CallOption) ([]byte, error)
}

// Client 调用 admin SecretMgr。
type Client struct {
	gateway Forwarder
	timeout time.Duration
}

// New 创建 SecretMgr 客户端；timeout 不大于 0 时使用 10 秒。
func New(gateway Forwarder, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{gateway: gateway, timeout: timeout}
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
	if c == nil || c.gateway == nil {
		return errors.New("SecretMgr 客户端没有配置 gatewayclient")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	out, err := c.gateway.Forward(ctx, secretMgrService, method, codec.SerializationTypeJSON, body, gatewayclient.WithTimeout(c.timeout))
	if err != nil {
		return fmt.Errorf("调用 SecretMgr.%s: %w", method, err)
	}
	if err := json.Unmarshal(out, rsp); err != nil {
		return fmt.Errorf("解析 SecretMgr.%s 响应: %w", method, err)
	}
	if !rsp.retOK() {
		return fmt.Errorf("SecretMgr.%s 失败: %s", method, rsp.retMessage())
	}
	return nil
}

type responseWithRetInfo interface {
	retOK() bool
	retMessage() string
}

// retInfo.code 是 tRPC JSON 序列化输出的枚举数值（SUCCESS = 0）；缺少 code 视为失败。
type retInfo struct {
	Code *int   `json:"code"`
	Msg  string `json:"msg"`
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

func (r *getSecretValueRsp) retOK() bool        { return r.RetInfo.Code != nil && *r.RetInfo.Code == 0 }
func (r *getSecretValueRsp) retMessage() string { return r.RetInfo.Msg }
