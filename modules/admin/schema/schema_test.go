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
	end := strings.Index(sql[start:], "-- ************ SSH 会话表")
	if end < 0 {
		t.Fatal("service deployment schema block terminator missing")
	}
	block := sql[start : start+end]
	for _, unwanted := range []string{"c_is_deleted", "c_base_url", "c_rpc_address"} {
		if strings.Contains(block, unwanted) {
			t.Fatalf("service deployment schema must not persist %s", unwanted)
		}
	}
	for _, required := range []string{
		"c_node_id TEXT NOT NULL",
		"c_gateway_service_id TEXT NOT NULL DEFAULT ''",
		"c_gateway_enabled INTEGER NOT NULL DEFAULT 0",
		"FOREIGN KEY (c_node_id) REFERENCES t_gateway_nodes(c_node_id)",
		"ON t_service_deployments(c_node_id, c_service_name)",
		"WHERE c_gateway_enabled = 1 AND c_gateway_service_id <> ''",
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("service deployment schema missing %q", required)
		}
	}
	if strings.Contains(block, "ON t_service_deployments(c_service_name);") {
		t.Fatal("service deployment name must be unique only within a gateway node")
	}
}

func TestAdminSchemaDefinesGatewayNodes(t *testing.T) {
	sql := AdminSQL()
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS t_gateway_nodes")
	if start < 0 {
		t.Fatal("gateway node schema block missing")
	}
	end := strings.Index(sql[start:], "CREATE TABLE IF NOT EXISTS t_service_deployments")
	if end < 0 {
		t.Fatal("gateway nodes must be declared before service deployments")
	}
	block := sql[start : start+end]
	for _, required := range []string{
		"c_node_id TEXT NOT NULL",
		"c_host_id INTEGER",
		"c_name TEXT NOT NULL",
		"c_public_address TEXT NOT NULL",
		"c_status TEXT NOT NULL DEFAULT 'enabled'",
		"CHECK (c_status IN ('enabled', 'disabled'))",
		"c_route_hash TEXT NOT NULL DEFAULT ''",
		"c_applied_route_hash TEXT NOT NULL DEFAULT ''",
		"c_route_count INTEGER NOT NULL DEFAULT 0",
		"c_last_seen_at DATETIME",
		"c_last_error TEXT NOT NULL DEFAULT ''",
		"FOREIGN KEY (c_host_id) REFERENCES t_ssh_host(c_id)",
		"update_gateway_nodes_mtime",
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("gateway node schema missing %q", required)
		}
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
