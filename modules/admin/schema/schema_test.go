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

func TestAdminSchemaContainsNoUserActionsUpgradePath(t *testing.T) {
	sql := AdminSQL()
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS t_user_actions",
		"DROP TABLE IF EXISTS t_user_actions",
	} {
		if strings.Contains(sql, statement) {
			t.Fatalf("admin schema must not contain retired user-actions statement %q", statement)
		}
	}
}

func TestAdminSchemaExcludesLegacyHostMonitorHistory(t *testing.T) {
	if strings.Contains(AdminSQL(), "t_host_monitor_history") {
		t.Fatal("admin schema must not create the legacy host monitor history table")
	}
}

func TestAdminSchemaSupportsSetupWithoutStateTable(t *testing.T) {
	sql := AdminSQL()
	if strings.Contains(sql, "t_system_setup") {
		t.Fatal("setup status must be derived from domain records, not t_system_setup")
	}
	for _, required := range []string{
		"idx_users_username",
		"idx_ssh_host_address",
		"idx_secrets_secret_id_deleted",
		"cloud=云厂商凭据",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("admin schema setup contract missing %q", required)
		}
	}
}

// TestAdminSchemaHasNoLegacyDeploymentTables 校验旧的网关节点表和服务部署表已删除，部署只记录在 t_hosts、t_placements。
func TestAdminSchemaHasNoLegacyDeploymentTables(t *testing.T) {
	sql := AdminSQL()
	for _, legacy := range []string{"t_gateway_nodes", "t_service_deployments"} {
		if strings.Contains(sql, legacy) {
			t.Fatalf("admin schema still defines legacy table %s", legacy)
		}
	}
	for _, table := range []string{"t_hosts", "t_placements", "t_host_gateway_status"} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Fatalf("admin schema missing %s", table)
		}
	}
}
