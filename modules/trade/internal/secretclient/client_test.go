package secretclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestGetExchangeSecretReadsOnlyConfiguredSecretWithServiceAuth(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		calls.Add(1)
		if request.URL.Path != "/api/service/secret/GetSecretValue" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("X-Moox-Signature") == "" ||
			request.Header.Get("X-Moox-Target-Node") != "gateway-gz-122" {
			t.Fatal("missing node-targeted gateway auth")
		}
		var body getSecretValueReq
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.SecretID != "sec_1" {
			t.Fatalf("secret_id = %q", body.SecretID)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0, "msg": "success"},
			"secret": map[string]any{
				"secret_id":     "sec_1",
				"name":          "Binance",
				"category":      "exchange",
				"pro" + "vider": "binance",
				"secret_type":   "api_key",
				"key_id":        "api-key",
				"secret_value":  "plain-secret",
				"status":        "active",
				"extra_config":  `{"market_type":"swap"}`,
			},
		})
	}))
	defer server.Close()

	client := New(Config{
		GatewayBaseURL: server.URL,
		ServiceAuth: ServiceAuthConfig{
			AccessKey: "access", SecretKey: "secret",
			TargetNode: "gateway-gz-122",
		},
	})
	secret, err := client.GetExchangeSecret(context.Background(), "sec_1")
	if err != nil {
		t.Fatalf("GetExchangeSecret() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls = %d, want 1", calls.Load())
	}
	if secret.SecretValue != "plain-secret" ||
		secret.KeyID != "api-key" ||
		secret.Exchange != exchange.ExchangeBinance {
		t.Fatalf("secret = %+v", secret)
	}
}

func TestGetExchangeSecretRejectsInvalidInputAndMetadata(t *testing.T) {
	client := New(Config{})
	if _, err := client.GetExchangeSecret(context.Background(), " "); !errors.Is(
		err,
		account.ErrInvalidCredential,
	) {
		t.Fatalf("empty secret ID error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"secret": map[string]any{
				"secret_id":     "different",
				"category":      "cloud",
				"pro" + "vider": "binance",
				"status":        "active",
				"key_id":        "key",
				"secret_value":  "value",
			},
		})
	}))
	defer server.Close()

	client = New(Config{
		GatewayBaseURL: server.URL,
		ServiceAuth: ServiceAuthConfig{
			AccessKey: "access", SecretKey: "secret", TargetNode: "gateway-test",
		},
	})
	if _, err := client.GetExchangeSecret(
		context.Background(),
		"secret-1",
	); !errors.Is(err, account.ErrInvalidCredential) {
		t.Fatalf("metadata error = %v, want ErrInvalidCredential", err)
	}
}
