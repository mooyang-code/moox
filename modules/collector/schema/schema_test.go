package schema

import (
	"os"
	"strings"
	"testing"
)

func TestMarketV2SchemaContainsCoordinationAndAttemptTables(t *testing.T) {
	raw, err := os.ReadFile("collector.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, table := range []string{"t_collector_market_leases", "t_collector_provider_quota_windows", "t_collector_provider_permits", "t_collector_task_attempts", "t_collector_attempt_subjects", "t_collector_attempt_outbox", "t_collector_provider_runtime", "t_collector_market_generations", "t_collector_control_leader"} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("schema missing %s", table)
		}
	}
	for _, index := range []string{"idx_market_lease_key", "idx_market_attempt_space_status", "idx_market_outbox_pending"} {
		if !strings.Contains(sql, index) {
			t.Errorf("schema missing index %s", index)
		}
	}
}
