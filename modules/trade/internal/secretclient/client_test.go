package secretclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

type gatewayFunc func(context.Context, string, string, int, []byte) ([]byte, error)

func (f gatewayFunc) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	return f(ctx, service, method, serialization, body)
}

func TestGetExchangeSecretReadsConfiguredSecretThroughNativeGateway(t *testing.T) {
	calls := 0
	client := New(gatewayFunc(func(_ context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
		calls++
		require.Equal(t, "trpc.moox.ops.SecretMgr", service)
		require.Equal(t, "GetSecretValue", method)
		require.Equal(t, codec.SerializationTypeJSON, serialization)
		var request getSecretValueReq
		require.NoError(t, json.Unmarshal(body, &request))
		require.Equal(t, "sec_1", request.SecretID)
		return json.Marshal(map[string]any{"ret_info": map[string]any{"code": 0}, "secret": map[string]any{
			"secret_id": "sec_1", "name": "Binance", "category": "exchange", "pro" + "vider": "binance",
			"key_id": "api-key", "secret_value": "plain-secret", "status": "active", "extra_config": `{}`,
		}})
	}))
	secret, err := client.GetExchangeSecret(t.Context(), "sec_1")
	require.NoError(t, err)
	require.Equal(t, "plain-secret", secret.SecretValue)
	require.Equal(t, "api-key", secret.KeyID)
	require.Equal(t, exchange.ExchangeBinance, secret.Exchange)
	require.Equal(t, 1, calls)
}

func TestGetExchangeSecretRejectsInvalidInputAndMetadata(t *testing.T) {
	_, err := New(nil).GetExchangeSecret(t.Context(), " ")
	require.ErrorIs(t, err, account.ErrInvalidCredential)
	client := New(gatewayFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
		return json.Marshal(map[string]any{"ret_info": map[string]any{"code": 0}, "secret": map[string]any{
			"secret_id": "different", "category": "cloud", "pro" + "vider": "binance", "status": "active", "key_id": "key", "secret_value": "value",
		}})
	}))
	_, err = client.GetExchangeSecret(t.Context(), "secret-1")
	require.ErrorIs(t, err, account.ErrInvalidCredential)
}

func TestGetExchangeSecretLeavesRetryToSharedGateway(t *testing.T) {
	calls := 0
	transportErr := errors.New("response lost")
	client := New(gatewayFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return nil, transportErr }))
	_, err := client.GetExchangeSecret(t.Context(), "secret-1")
	require.ErrorIs(t, err, transportErr)
	require.Equal(t, 1, calls)
	_, err = New(nil).GetExchangeSecret(t.Context(), "secret-1")
	require.Error(t, err)
}
