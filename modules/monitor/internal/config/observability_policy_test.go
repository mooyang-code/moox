package config

import (
	"strings"
	"testing"
)

func TestObservabilityDeliveryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, document, env, want string
		invalid                   bool
	}{
		{name: "default", document: "{}", want: "all"},
		{name: "yaml", document: "observability:\n  deliver_policy: new\n", want: "new"},
		{name: "environment", document: "{}", env: " NEW ", want: "new"},
		{name: "override", document: "observability:\n  deliver_policy: new\n", env: "all", want: "all"},
		{name: "invalid yaml", document: "observability:\n  deliver_policy: neww\n", invalid: true},
		{name: "invalid environment", document: "{}", env: "neww", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
			t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
			t.Setenv("MOOX_OBSERVABILITY_DELIVER_POLICY", tc.env)
			cfg, err := Load(writeConfig(t, tc.document))
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "observability.deliver_policy") {
					t.Fatalf("expected policy validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Observability.DeliverPolicy != tc.want {
				t.Fatalf("expected policy %s, got %s", tc.want, cfg.Observability.DeliverPolicy)
			}
		})
	}
}
