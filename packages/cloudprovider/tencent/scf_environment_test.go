package tencent

import (
	"strings"
	"testing"
)

func TestValidateSCFEnvironmentIncludesAllCredentialsAndRedactsValues(t *testing.T) {
	values := map[string]string{"PROVIDER_SECRET_KEY": strings.Repeat("secret-value", 400), "MOOX_ACCESS_ADDRESS": "10.206.0.5:11004"}
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

func validCollectorEnvironment() map[string]string {
	return map[string]string{
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`,
		"MOOX_SPACE_ID": "stockcn", "MOOX_CODE_PACKAGE_ID": "collector_dev_00000000-0000-0000-0000-000000000000",
		"MOOX_CALLER": "scf-collector", "MOOX_CALLER_KEY": "scf-collector-1:private-test-secret",
		"MOOX_ACCESS_ADDRESS": "10.206.0.5:11004", "MOOX_ACCESS_ID": "access@storage",
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "topic", "MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "cls-id", "MOOX_CLS_SECRET_KEY": "cls-secret",
		"MOOX_EVENTBUS_NATS_URL": "tls://eventbus.example:4222", "MOOX_EVENTBUS_NATS_USERNAME": "collector", "MOOX_EVENTBUS_NATS_PASSWORD": "bus-secret", "MOOX_EVENTBUS_NATS_TLS_CA_FILE": "certs/eventbus-ca.pem",
	}
}

func TestCollectorMarketFetchEnvironmentRejectsIncompleteOrInvalidValues(t *testing.T) {
	valid := validCollectorEnvironment()
	if err := ValidateCollectorMarketFetchEnvironment(valid); err != nil {
		t.Fatal(err)
	}
	withLegacyCA := validCollectorEnvironment()
	withLegacyCA["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = "cGVt"
	if err := ValidateCollectorMarketFetchEnvironment(withLegacyCA); err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("不能同时配置 CA 文件和内嵌 PEM，实际 %v", err)
	}
	for _, test := range []struct {
		name, appKeys string
		wantError     bool
	}{
		{"other binding only", `{"other":"` + strings.Repeat("b", 64) + `"}`, true},
		{"Collector binding with extra key", `{"other":"` + strings.Repeat("b", 64) + `","moox-collector":"` + strings.Repeat("a", 64) + `"}`, false},
	} {
		t.Run("Storage binding/"+test.name, func(t *testing.T) {
			values := validCollectorEnvironment()
			values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"] = test.appKeys
			err := ValidateCollectorMarketFetchEnvironment(values)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "moox-collector") || strings.Contains(err.Error(), strings.Repeat("b", 64)) {
					t.Fatalf("缺少 Collector 绑定时必须报错且不暴露密钥: %v", err)
				}
			} else if err != nil {
				t.Fatalf("Collector 绑定加额外的 key 是合法的: %v", err)
			}
		})
	}
	for key := range valid {
		t.Run("missing "+key, func(t *testing.T) {
			values := validCollectorEnvironment()
			delete(values, key)
			if err := ValidateCollectorMarketFetchEnvironment(values); err == nil {
				t.Fatalf("缺少 %s 时必须报错", key)
			}
		})
	}
	for _, test := range []struct{ key, value string }{
		{"MOOX_CALLER", "collector"}, {"MOOX_CALLER_KEY", "no-separator"}, {"MOOX_CALLER_KEY", "scf-collector-1:"},
		{"MOOX_ACCESS_ADDRESS", "10.206.0.5"}, {"MOOX_ACCESS_ADDRESS", "127.0.0.1:11004"}, {"MOOX_ACCESS_ADDRESS", "localhost:11004"},
		{"MOOX_ACCESS_ADDRESS", "10.206.0.5:0"}, {"MOOX_ACCESS_ID", "storage"}, {"MOOX_ACCESS_ID", "access@bad node"},
		{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{}`}, {"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{"moox-collector":"short"}`},
		{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{"moox-collector":"` + strings.Repeat("g", 64) + `"}`},
		{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", `{"moox-collector":"` + strings.Repeat("a", 64) + `","moox-collector":"` + strings.Repeat("b", 64) + `"}`},
		{"MOOX_EVENTBUS_NATS_URL", "nats://eventbus.example:4222"}, {"MOOX_EVENTBUS_NATS_URL", "tls://127.0.0.1:4222"},
		{"MOOX_CLS_ENABLED", "false"}, {"MOOX_CLS_TIMEOUT_MS", "invalid"}, {"MOOX_EVENTBUS_NATS_TLS_CA_FILE", "/tmp/ca.pem"},
	} {
		t.Run("invalid "+test.key+" "+test.value, func(t *testing.T) {
			values := validCollectorEnvironment()
			values[test.key] = test.value
			err := ValidateCollectorMarketFetchEnvironment(values)
			if err == nil {
				t.Fatalf("%s=%q 必须报错", test.key, test.value)
			}
			if strings.Contains(err.Error(), "private-test-secret") {
				t.Fatalf("报错不能暴露调用方密钥: %v", err)
			}
		})
	}
}
