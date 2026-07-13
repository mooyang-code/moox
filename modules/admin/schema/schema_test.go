package schema

import (
	"strings"
	"testing"
)

func TestAdminSchemaUsesBoolSoftDelete(t *testing.T) {
	sql := AdminSQL()
	if strings.Contains(sql, "c_is_deleted TEXT") {
		t.Fatalf("admin schema must not store c_is_deleted as TEXT")
	}
	for _, line := range strings.Split(sql, "\n") {
		if strings.Contains(line, "c_is_deleted") &&
			(strings.Contains(line, "DEFAULT 'false'") || strings.Contains(line, "DEFAULT 'true'")) {
			t.Fatalf("admin schema must not use string defaults for c_is_deleted")
		}
	}
	if got := strings.Count(sql, "c_is_deleted INTEGER NOT NULL DEFAULT 0"); got != 3 {
		t.Fatalf("expected 3 bool soft-delete columns, got %d", got)
	}
}

func TestServiceDeploymentsStoresOnlyCurrentRows(t *testing.T) {
	sql := AdminSQL()
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS t_service_deployments")
	if start < 0 {
		t.Fatal("service deployment schema block missing")
	}
	end := strings.Index(sql[start:], "-- ************ 用户表")
	if end < 0 {
		t.Fatal("service deployment schema block terminator missing")
	}
	block := sql[start : start+end]
	for _, unwanted := range []string{"c_is_deleted", "c_base_url", "c_rpc_address"} {
		if strings.Contains(block, unwanted) {
			t.Fatalf("service deployment schema must not persist %s", unwanted)
		}
	}
	if !strings.Contains(block, "idx_service_deployments_name") {
		t.Fatal("service deployment schema must enforce one current row per service_name")
	}
}

func TestAdminSchemaDropsLegacyUserActionsTable(t *testing.T) {
	sql := AdminSQL()
	if strings.Contains(sql, "CREATE TABLE IF NOT EXISTS t_user_actions") {
		t.Fatal("admin schema must not recreate t_user_actions")
	}
	if !strings.Contains(sql, "DROP TABLE IF EXISTS t_user_actions") {
		t.Fatal("admin schema must drop legacy t_user_actions")
	}
}

func TestAdminSchemaExcludesLegacyHostMonitorHistory(t *testing.T) {
	if strings.Contains(AdminSQL(), "t_host_monitor_history") {
		t.Fatal("admin schema must not create the legacy host monitor history table")
	}
}
