package secretclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
					"secret_id":    "sec_1",
					"name":         "币安-1",
					"category":     "exchange",
					"provider":     "binance",
					"secret_type":  "api_key",
					"key_id":       "api-key",
					"secret_value": "masked",
					"status":       "active",
					"extra_config": `{"market_type":"swap"}`,
				}},
			})
		case "/api/service/secret/RevealSecret":
			seenRevealAuth = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info": map[string]any{"code": 0, "msg": "success"},
				"secret": map[string]any{
					"secret_id":    "sec_1",
					"name":         "币安-1",
					"provider":     "binance",
					"key_id":       "api-key",
					"secret_value": "plain-secret",
					"extra_config": `{"market_type":"swap"}`,
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
	secrets, err := client.ListExchangeSecrets(context.Background(), "binance")
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
}
