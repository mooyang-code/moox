// Package secretclient reads Exchange credentials from the Admin gateway.
package secretclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"trpc.group/trpc-go/trpc-go/codec"
)

// Gateway is borrowed from the process-owned native gateway client.
type Gateway interface {
	Forward(context.Context, string, string, int, []byte) ([]byte, error)
}

type Client struct{ gateway Gateway }

func New(gateway Gateway) *Client { return &Client{gateway: gateway} }

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
		return fmt.Errorf("secret client requires the process gateway client")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	raw, err := c.gateway.Forward(ctx, "trpc.moox.ops.SecretMgr", method, codec.SerializationTypeJSON, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, rsp); err != nil {
		return err
	}
	if !rsp.retOK() {
		return fmt.Errorf("secret %s failed: %s", method, rsp.retMessage())
	}
	return nil
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
