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

func TestCollectorMarketFetchEnvironmentRejectsIncompleteOrInvalidValues(t *testing.T) {
	valid := map[string]string{
		"MOOX_CALLER": "scf-collector", "MOOX_ENTRY_ADDRESS": "storage.example:11004", "MOOX_ENTRY_HOST": "storage", "MOOX_CALLER_KEY_ID": "assigned-scf-key-17", "MOOX_CALLER_KEY": strings.Repeat("a", 43) + "=",
		"MOOX_SPACE_ID": "stockcn", "MOOX_CODE_PACKAGE_ID": "collector_dev_00000000-0000-0000-0000-000000000000",
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "topic", "MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "cls-id", "MOOX_CLS_SECRET_KEY": "cls-secret",
	}
	clone := func() map[string]string {
		copy := make(map[string]string, len(valid))
		for k, v := range valid {
			copy[k] = v
		}
		return copy
	}
	if err := ValidateCollectorMarketFetchEnvironment(valid); err != nil {
		t.Fatal(err)
	}
	// 定时采集函数带采集计划，补采函数带运行方式，规则相同。
	withPlan := clone()
	withPlan["MOOX_MARKET_FETCH_PLAN"] = `{"plan_id":"p1"}`
	if err := ValidateCollectorMarketFetchEnvironment(withPlan); err != nil {
		t.Fatal(err)
	}
	backfill := clone()
	backfill["MOOX_MARKET_FETCH_MODE"] = "backfill"
	if err := ValidateCollectorMarketFetchEnvironment(backfill); err != nil {
		t.Fatal(err)
	}
	for key := range valid {
		t.Run("missing "+key, func(t *testing.T) {
			copy := clone()
			delete(copy, key)
			if err := ValidateCollectorMarketFetchEnvironment(copy); err == nil {
				t.Fatalf("missing %s must fail closed", key)
			}
		})
	}
	for _, test := range []struct{ key, value string }{
		{"MOOX_CALLER", "collector"},
		{"MOOX_ENTRY_ADDRESS", "localhost:11004"}, {"MOOX_ENTRY_ADDRESS", "storage.example:0"}, {"MOOX_ENTRY_ADDRESS", "ip://storage.example:11004"}, {"MOOX_ENTRY_ADDRESS", "storage.example:11004/path"},
		{"MOOX_ENTRY_HOST", "access@storage"}, {"MOOX_ENTRY_HOST", "bad_node"}, {"MOOX_CALLER_KEY_ID", "bad key"}, {"MOOX_CALLER_KEY_ID", ""}, {"MOOX_CALLER_KEY", "short"}, {"MOOX_CALLER_KEY", strings.Repeat("a", 4097)}, {"MOOX_CALLER_KEY", strings.Repeat("a", 32) + "\n"},
		{"MOOX_CLS_ENABLED", "false"}, {"MOOX_CLS_TIMEOUT_MS", "invalid"}, {"MOOX_MARKET_FETCH_PLAN", strings.Repeat("p", 64<<10+1)},
	} {
		t.Run("invalid "+test.key+" "+test.value[:min(len(test.value), 40)], func(t *testing.T) {
			copy := clone()
			copy[test.key] = test.value
			if err := ValidateCollectorMarketFetchEnvironment(copy); err == nil {
				t.Fatalf("invalid %s must fail closed", test.key)
			}
		})
	}
	// 函数不连 EventBus：出现任何 EventBus 变量都拒绝，报错里带变量名、不带值。
	for _, key := range []string{
		"MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_NATS_TLS_CA_FILE", "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64",
		"MOOX_METRICS_EVENTBUS_URL", "MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE", "NATS_URL",
	} {
		t.Run("eventbus "+key, func(t *testing.T) {
			copy := clone()
			copy[key] = "bus-secret"
			err := ValidateCollectorMarketFetchEnvironment(copy)
			if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "bus-secret") {
				t.Fatalf("EventBus field %s must be rejected without its value, got %v", key, err)
			}
		})
	}
}
