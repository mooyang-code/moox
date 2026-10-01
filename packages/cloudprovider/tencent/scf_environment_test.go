package tencent

import (
	"strings"
	"testing"
)

func TestValidateSCFEnvironmentIncludesAllCredentialsAndRedactsValues(t *testing.T) {
	values := map[string]string{"PROVIDER_SECRET_KEY": strings.Repeat("secret-value", 400), "MOOX_COLLECTOR_RPC_GATEWAY_TARGET": "ip://collector:11002"}
	err := ValidateSCFEnvironment(values)
	if err == nil || !strings.Contains(err.Error(), "PROVIDER_SECRET_KEY") || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("expected redacted total environment validation, got %v", err)
	}
	if err := ValidateSCFEnvironment(map[string]string{"A": strings.Repeat("a", 4092)}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSCFEnvironmentRejectsPrimaryMasterSecret(t *testing.T) {
	for _, secret := range []string{"", "test-master-secret"} {
		err := ValidateSCFEnvironment(map[string]string{"MOOX_STORAGE_PRIMARY_AUTH_SECRET": secret})
		if err == nil || !strings.Contains(err.Error(), "MOOX_STORAGE_PRIMARY_AUTH_SECRET") || strings.Contains(err.Error(), "test-master-secret") {
			t.Fatalf("SCF must reject the master key without disclosure: %v", err)
		}
	}
}

func TestCollectorTimerEnvironmentRejectsIncompleteOrInvalidManagedValues(t *testing.T) {
	valid := map[string]string{
		"MOOX_GATEWAY_CALLER":                     "collector",
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`,
		"MOOX_SPACE_ID":                           "stockcn", "MOOX_CODE_PACKAGE_ID": "collector_dev_00000000-0000-0000-0000-000000000000",
		"MOOX_GATEWAY_NODE_ID": "storage", "MOOX_GATEWAY_TARGET_NODE": "storage", "MOOX_GATEWAY_SERVICE_KEY_ID": "collector", "MOOX_GATEWAY_SERVICE_SECRET_KEY": "private-test-secret",
		"MOOX_STORAGE_RPC_GATEWAY_TARGET": "ip://storage.example:11003", "MOOX_COLLECTOR_RPC_GATEWAY_TARGET": "ip://collector.example:11004", "MOOX_COLLECTOR_GATEWAY_TARGET_NODE": "collector",
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "topic", "MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "cls-id", "MOOX_CLS_SECRET_KEY": "cls-secret",
		"MOOX_EVENTBUS_NATS_URL": "tls://eventbus.example:4222", "MOOX_EVENTBUS_NATS_USERNAME": "collector", "MOOX_EVENTBUS_NATS_PASSWORD": "bus-secret", "MOOX_EVENTBUS_NATS_TLS_CA_FILE": "certs/eventbus-ca.pem",
	}
	if err := ValidateCollectorTimerEnvironment(valid); err != nil {
		t.Fatal(err)
	}
	withLegacyCA := make(map[string]string)
	for key, value := range valid {
		withLegacyCA[key] = value
	}
	withLegacyCA["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = "cGVt"
	if err := ValidateCollectorTimerEnvironment(withLegacyCA); err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("Timer must reject simultaneous CA file and embedded PEM, got %v", err)
	}
	invoke := make(map[string]string)
	for key, value := range valid {
		invoke[key] = value
	}
	delete(invoke, "MOOX_COLLECTOR_RPC_GATEWAY_TARGET")
	delete(invoke, "MOOX_COLLECTOR_GATEWAY_TARGET_NODE")
	invoke["MOOX_FETCH_TIMEOUT_SECONDS"] = "90"
	if err := ValidateCollectorMarketFetchEnvironment(invoke); err != nil {
		t.Fatalf("Invoke has its own timeout and no Claim route: %v", err)
	}
	if err := ValidateCollectorTimerEnvironment(invoke); err == nil {
		t.Fatal("Timer must still require its Claim route")
	}
	for _, test := range []struct {
		name, appKeys string
		wantError     bool
	}{
		{"other binding only", `{"other":"` + strings.Repeat("b", 64) + `"}`, true},
		{"Collector binding with extra key", `{"other":"` + strings.Repeat("b", 64) + `","moox-collector":"` + strings.Repeat("a", 64) + `"}`, false},
	} {
		for _, validator := range []struct {
			name     string
			validate func(map[string]string) error
		}{
			{"Invoke", ValidateCollectorMarketFetchEnvironment},
			{"Timer", ValidateCollectorTimerEnvironment},
		} {
			t.Run("Storage binding/"+validator.name+"/"+test.name, func(t *testing.T) {
				values := make(map[string]string)
				for key, value := range valid {
					values[key] = value
				}
				values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"] = test.appKeys
				err := validator.validate(values)
				if test.wantError {
					if err == nil || !strings.Contains(err.Error(), "moox-collector") || strings.Contains(err.Error(), strings.Repeat("b", 64)) {
						t.Fatalf("missing Collector binding must fail without exposing keys: %v", err)
					}
				} else if err != nil {
					t.Fatalf("Collector binding plus extra keys is valid: %v", err)
				}
			})
		}
	}
	for key := range valid {
		t.Run("missing "+key, func(t *testing.T) {
			copy := make(map[string]string)
			for k, v := range valid {
				copy[k] = v
			}
			delete(copy, key)
			if err := ValidateCollectorTimerEnvironment(copy); err == nil {
				t.Fatalf("missing %s must fail closed", key)
			}
		})
	}
	for _, test := range []struct{ key, value string }{
		{"MOOX_GATEWAY_CALLER", "strategy"},
		{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{}`}, {"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{"moox-collector":"short"}`},
		{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{"moox-collector":"` + strings.Repeat("g", 64) + `"}`},
		{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{"moox-collector":"` + strings.Repeat("a", 64) + `","moox-collector":"` + strings.Repeat("b", 64) + `"}`},
		{"MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://localhost:11003"}, {"MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "ip://collector.example:0"},
		{"MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "ip://collector.example:11004/path"}, {"MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "bad node"},
		{"MOOX_EVENTBUS_NATS_URL", "nats://eventbus.example:4222"}, {"MOOX_CLS_ENABLED", "false"}, {"MOOX_CLS_TIMEOUT_MS", "invalid"}, {"MOOX_EVENTBUS_NATS_TLS_CA_FILE", "/tmp/ca.pem"},
	} {
		t.Run("invalid "+test.key+" "+test.value, func(t *testing.T) {
			copy := make(map[string]string)
			for k, v := range valid {
				copy[k] = v
			}
			copy[test.key] = test.value
			if err := ValidateCollectorTimerEnvironment(copy); err == nil {
				t.Fatalf("invalid %s must fail closed", test.key)
			}
		})
	}
}
