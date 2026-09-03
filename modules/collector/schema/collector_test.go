package schema

import (
	"regexp"
	"strings"
	"testing"
)

func TestTaskInstanceSchemaUsesShortLivedMarketDimensions(t *testing.T) {
	definition := taskInstanceTableDefinition(t)
	for _, forbidden := range []string{"c_cloud_job_item_id", "c_exchange", "c_market", "c_symbol", "c_interval"} {
		if regexp.MustCompile(`(?m)^\s*` + forbidden + `\s`).MatchString(definition) {
			t.Fatalf("legacy TaskInstance column %s remains in schema", forbidden)
		}
	}
	for _, required := range []string{"c_provider", "c_market_type", "c_frequency", "c_source_id", "c_function_name"} {
		if !regexp.MustCompile(`(?m)^\s*` + required + `\s`).MatchString(definition) {
			t.Fatalf("short-lived TaskInstance column %s is missing", required)
		}
	}
}

func taskInstanceTableDefinition(t *testing.T) string {
	t.Helper()
	const marker = "CREATE TABLE IF NOT EXISTS t_collector_task_instances ("
	sql := AllSQL()
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatal("task instance table is missing")
	}
	definition := sql[start:]
	end := strings.Index(definition, "\n);")
	if end < 0 {
		t.Fatal("task instance table terminator is missing")
	}
	return definition[:end]
}
