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
	if got := strings.Count(sql, "c_is_deleted INTEGER NOT NULL DEFAULT 0"); got != 2 {
		t.Fatalf("expected 2 bool soft-delete columns, got %d", got)
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
		`INSERT INTO t_deployments (c_host_id, c_component_id) VALUES ('missing', 'admin')`,
		`INSERT INTO t_deployments (c_host_id, c_component_id, c_status) VALUES ('control', 'admin', 'active')`,
		`INSERT INTO t_host_gateway_status (c_host_id) VALUES ('missing')`,
		`INSERT INTO t_host_gateway_status (c_host_id, c_route_count) VALUES ('control', -1)`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("schema accepted invalid input: %s", statement)
		}
	}
	if _, err := db.Exec(`INSERT INTO t_deployments (c_host_id, c_component_id) VALUES ('control', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM t_hosts WHERE c_host_id = 'control'`); err == nil {
		t.Fatal("host with deployments was deleted without removing dependent rows")
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
		"idx_secrets_secret_id_deleted",
		"cloud=云厂商凭据",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("admin schema setup contract missing %q", required)
		}
	}
}

func TestAdminSchemaExcludesRetiredSingleUserAndSSHTables(t *testing.T) {
	sql := AdminSQL()
	for _, table := range []string{"t_space_members", "t_login_history", "t_ssh_host", "t_ssh_host_key", "t_ssh_session"} {
		if strings.Contains(sql, table) {
			t.Fatalf("retired table remains in admin schema: %s", table)
		}
	}
	for _, column := range []string{"c_role", "c_nickname", "c_email", "c_avatar", "c_last_used_at", "c_last_used_by"} {
		if strings.Contains(sql, column) {
			t.Fatalf("retired column remains in admin schema: %s", column)
		}
	}
}

func TestHostSchemaCarriesLabelAndSSHColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(AdminSQL()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_hosts (c_host_id, c_label, c_address, c_ssh_port, c_ssh_user) VALUES ('control', '控制机', 'control.example.test', 22, 'ubuntu')`); err != nil {
		t.Fatal(err)
	}
	var port int
	var fingerprint string
	if err := db.QueryRow(`SELECT c_ssh_port, c_ssh_fingerprint FROM t_hosts WHERE c_host_id = 'control'`).Scan(&port, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if port != 22 || fingerprint != "" {
		t.Fatalf("unexpected ssh columns: port=%d fingerprint=%q", port, fingerprint)
	}
	if _, err := db.Exec(`INSERT INTO t_hosts (c_host_id, c_address, c_ssh_port) VALUES ('bad', 'bad.example.test', 70000)`); err == nil {
		t.Fatal("schema accepted an out-of-range ssh port")
	}
}
