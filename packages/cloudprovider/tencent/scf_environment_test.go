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
