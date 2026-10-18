package schema

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
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

func TestHostTopologySchemaEnforcesReferencesAndStatuses(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(AdminSQL()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_hosts (c_host_id, c_address) VALUES ('control', 'control.example.test')`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO t_hosts (c_host_id, c_address, c_status) VALUES ('invalid', 'invalid.example.test', 'active')`,
		`INSERT INTO t_hosts (c_host_id, c_address) VALUES ('duplicate', 'control.example.test')`,
		`INSERT INTO t_placements (c_host_id, c_component_id) VALUES ('missing', 'admin')`,
		`INSERT INTO t_placements (c_host_id, c_component_id, c_status) VALUES ('control', 'admin', 'active')`,
		`INSERT INTO t_host_gateway_status (c_host_id) VALUES ('missing')`,
		`INSERT INTO t_host_gateway_status (c_host_id, c_route_count) VALUES ('control', -1)`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("schema accepted invalid input: %s", statement)
		}
	}
	if _, err := db.Exec(`INSERT INTO t_placements (c_host_id, c_component_id) VALUES ('control', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM t_hosts WHERE c_host_id = 'control'`); err == nil {
		t.Fatal("host with placements was deleted without removing dependent rows")
	}
}

func TestAdminSchemaExcludesRetiredDeploymentTables(t *testing.T) {
	for _, table := range []string{"t_service_deployments", "t_gateway_nodes"} {
		if strings.Contains(AdminSQL(), table) {
			t.Fatalf("retired deployment table remains: %s", table)
		}
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
