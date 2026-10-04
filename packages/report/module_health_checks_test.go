package report

import "testing"

func TestBuiltInModuleHealthChecksAreUniqueAndModuleScoped(t *testing.T) {
	checks := BuiltInModuleHealthChecks()
	if len(checks) == 0 {
		t.Fatal("built-in module health checks are empty")
	}
	seen := make(map[string]bool, len(checks))
	for _, check := range checks {
		if check.ID == "" || check.Module == "" {
			t.Fatalf("health check requires ID and module: %+v", check)
		}
		if seen[check.ID] {
			t.Fatalf("duplicate health check ID %q", check.ID)
		}
		seen[check.ID] = true
	}
	got := HealthCheckIDsForModule("monitor")
	if len(got) != 1 || got[0] != "monitor-metrics" {
		t.Fatalf("monitor health check IDs = %v", got)
	}
}
