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
	if got := strings.Count(sql, "c_is_deleted INTEGER NOT NULL DEFAULT 0"); got != 4 {
		t.Fatalf("expected 4 bool soft-delete columns, got %d", got)
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
