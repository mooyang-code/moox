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

func TestCollectorTimerEnvironmentRejectsIncompleteOrInvalidManagedValues(t *testing.T) {
	valid := map[string]string{
		"MOOX_CALLER": "scf-collector", "MOOX_ACCESS_ADDRESS": "storage.example:11004", "MOOX_ACCESS_ID": "access@storage", "MOOX_CALLER_KEY_ID": "assigned-scf-key-17", "MOOX_CALLER_KEY": strings.Repeat("a", 64),
		"MOOX_SPACE_ID": "stockcn", "MOOX_CODE_PACKAGE_ID": "collector_dev_00000000-0000-0000-0000-000000000000",
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
	invoke["MOOX_FETCH_TIMEOUT_SECONDS"] = "90"
	if err := ValidateCollectorMarketFetchEnvironment(invoke); err != nil {
		t.Fatalf("Invoke has its own timeout and shares Access: %v", err)
	}
	if err := ValidateCollectorTimerEnvironment(invoke); err != nil {
		t.Fatalf("Timer shares the same Access connection: %v", err)
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
		{"MOOX_CALLER", "collector"},
		{"MOOX_ACCESS_ADDRESS", "localhost:11004"}, {"MOOX_ACCESS_ADDRESS", "storage.example:0"}, {"MOOX_ACCESS_ADDRESS", "ip://storage.example:11004"}, {"MOOX_ACCESS_ADDRESS", "storage.example:11004/path"},
		{"MOOX_ACCESS_ID", "storage"}, {"MOOX_ACCESS_ID", "access@bad_node"}, {"MOOX_CALLER_KEY_ID", "bad key"}, {"MOOX_CALLER_KEY_ID", ""}, {"MOOX_CALLER_KEY", "short"}, {"MOOX_CALLER_KEY", strings.Repeat("a", 4097)}, {"MOOX_CALLER_KEY", strings.Repeat("a", 32) + "\n"},
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
