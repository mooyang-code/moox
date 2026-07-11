package schema

import (
	"strings"
	"testing"
)

func TestFrontendSchemaHasRebuildableReadModels(t *testing.T) {
	sql := AllSQL()
	for _, name := range []string{
		"t_strategy_run_metrics",
		"t_strategy_binding_health",
		"t_strategy_performance_points",
		"t_strategy_performance_daily",
		"t_strategy_operation_audits",
	} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+name) {
			t.Fatalf("missing table %s", name)
		}
	}
	for _, index := range []string{"idx_strategy_performance_points_time", "idx_strategy_performance_daily_date", "idx_strategy_operation_audits_binding"} {
		if !strings.Contains(sql, index) {
			t.Fatalf("missing index %s", index)
		}
	}
}
