package secretclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestListExchangeSecretsRevealsPlainSecretWithServiceAuth(t *testing.T) {
	var seenListAuth, seenRevealAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Moox-Signature") == "" || r.Header.Get("X-Moox-Target-Node") != "gateway-gz-122" {
			t.Fatalf("%s missing node-targeted gateway auth", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/service/secret/ListSecrets":
			seenListAuth = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info": map[string]any{"code": 0, "msg": "success"},
				"secrets": []map[string]any{{
					"secret_id":     "sec_1",
					"name":          "币安-1",
					"category":      "exchange",
					"pro" + "vider": "binance",
					"secret_type":   "api_key",
					"key_id":        "api-key",
					"secret_value":  "masked",
					"status":        "active",
					"extra_config":  `{"market_type":"swap"}`,
				}},
			})
		case "/api/service/secret/RevealSecret":
			seenRevealAuth = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info": map[string]any{"code": 0, "msg": "success"},
				"secret": map[string]any{
					"secret_id":     "sec_1",
					"name":          "币安-1",
					"pro" + "vider": "binance",
					"key_id":        "api-key",
					"secret_value":  "plain-secret",
					"extra_config":  `{"market_type":"swap"}`,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Config{
		GatewayBaseURL: srv.URL,
		ServiceAuth: ServiceAuthConfig{
			AccessKey:  "access",
			SecretKey:  "secret",
			TargetNode: "gateway-gz-122",
		},
	})
	secrets, err := client.ListExchangeSecrets(context.Background(), exchange.ExchangeBinance)
	if err != nil {
		t.Fatalf("ListExchangeSecrets returned error: %v", err)
	}
	if !seenListAuth || !seenRevealAuth {
		t.Fatalf("expected both ListSecrets and RevealSecret calls")
	}
	if len(secrets) != 1 {
		t.Fatalf("secrets len = %d, want 1", len(secrets))
	}
	if secrets[0].SecretValue != "plain-secret" {
		t.Fatalf("secret value = %q, want plaintext", secrets[0].SecretValue)
	}
	if secrets[0].KeyID != "api-key" {
		t.Fatalf("key_id = %q", secrets[0].KeyID)
	}
	if secrets[0].Category != "exchange" ||
		secrets[0].Exchange != exchange.ExchangeBinance ||
		secrets[0].Status != "active" {
		t.Fatalf("secret metadata = %+v", secrets[0])
	}
}

func TestListExchangeSecretsRejectsUnsupportedExchange(t *testing.T) {
	client := New(Config{})
	_, err := client.ListExchangeSecrets(context.Background(), exchange.Exchange("OTHER"))
	if !errors.Is(err, account.ErrInvalidCredential) {
		t.Fatalf("ListExchangeSecrets() error = %v, want ErrInvalidCredential", err)
	}
}

func TestListExchangeSecretsRejectsMismatchedMetadataBeforeReveal(t *testing.T) {
	revealCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/service/secret/ListSecrets" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info": map[string]any{"code": 0},
				"secrets": []map[string]any{{
					"secret_id":     "secret-1",
					"category":      "cloud",
					"pro" + "vider": "binance",
					"status":        "active",
				}},
			})
			return
		}
		revealCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
		})
	}))
	defer srv.Close()

	client := New(Config{
		GatewayBaseURL: srv.URL,
		ServiceAuth: ServiceAuthConfig{
			AccessKey: "access", SecretKey: "secret", TargetNode: "gateway-test",
		},
	})
	_, err := client.ListExchangeSecrets(context.Background(), exchange.ExchangeBinance)
	if !errors.Is(err, account.ErrInvalidCredential) {
		t.Fatalf("ListExchangeSecrets() error = %v, want ErrInvalidCredential", err)
	}
	if revealCalls != 0 {
		t.Fatalf("RevealSecret calls = %d, want 0", revealCalls)
	}
}

func TestValidateLiveCredentialAccessFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "empty"},
		{name: "short", key: "too-short"},
		{name: "old checked in literal", key: "moox-cloud-secret-key-32bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New(Config{EncryptionKey: tt.key})
			if !errors.Is(client.ValidateLiveCredentialAccess(), account.ErrLiveCredentialAccess) {
				t.Fatalf(
					"ValidateLiveCredentialAccess() error = %v, want ErrLiveCredentialAccess",
					client.ValidateLiveCredentialAccess(),
				)
			}
		})
	}

	client := New(Config{EncryptionKey: "0123456789abcdef0123456789abcdef"})
	if err := client.ValidateLiveCredentialAccess(); err != nil {
		t.Fatalf("ValidateLiveCredentialAccess() error = %v", err)
	}
}
