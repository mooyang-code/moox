package schema

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAdminSchemaUsesHardDeleteAndInitialSecretTombstone(t *testing.T) {
	raw := AdminSQL()
	if strings.Contains(raw, "c_is_deleted") {
		t.Fatalf("admin schema must not keep soft-delete column c_is_deleted")
	}
	if strings.Contains(raw, "c_expected_hash") {
		t.Fatalf("admin schema must not keep write-only column c_expected_hash")
	}
	for _, required := range []string{
		"UNIQUE (c_secret_id)",
		"t_deleted_initial_secrets",
		"c_ctime INTEGER NOT NULL",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(raw, required) {
			t.Fatalf("admin schema missing %q", required)
		}
	}
}

func TestHostTopologySchemaEnforcesReferencesAndCascades(t *testing.T) {
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
		`INSERT INTO t_host_gateway_status (c_host_id, c_expected_hash) VALUES ('control', 'deadbeef')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("schema accepted invalid input: %s", statement)
		}
	}
	if _, err := db.Exec(`INSERT INTO t_deployments (c_host_id, c_component_id) VALUES ('control', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_host_gateway_status (c_host_id, c_applied_hash) VALUES ('control', 'abc')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM t_hosts WHERE c_host_id = 'control'`); err != nil {
		t.Fatalf("deleting host should cascade to dependent tables: %v", err)
	}
	for _, table := range []string{"t_deployments", "t_host_gateway_status"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected cascade delete on %s, got %d remaining rows", table, count)
		}
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
	raw := AdminSQL()
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS t_user_actions",
		"DROP TABLE IF EXISTS t_user_actions",
	} {
		if strings.Contains(raw, statement) {
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
	raw := AdminSQL()
	if strings.Contains(raw, "t_system_setup") {
		t.Fatal("setup status must be derived from domain records, not t_system_setup")
	}
	for _, required := range []string{
		"idx_users_username",
		"UNIQUE (c_secret_id)",
		"cloud=云厂商凭据",
	} {
		if !strings.Contains(raw, required) {
			t.Fatalf("admin schema setup contract missing %q", required)
		}
	}
}

func TestAdminSchemaExcludesRetiredSingleUserAndSSHTables(t *testing.T) {
	raw := AdminSQL()
	for _, table := range []string{"t_space_members", "t_login_history", "t_ssh_host", "t_ssh_host_key", "t_ssh_session"} {
		if strings.Contains(raw, table) {
			t.Fatalf("retired table remains in admin schema: %s", table)
		}
	}
	for _, column := range []string{"c_role", "c_nickname", "c_email", "c_avatar", "c_last_used_at", "c_last_used_by"} {
		if strings.Contains(raw, column) {
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
