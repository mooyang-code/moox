package schema

import (
	"strings"
	"testing"
)

func TestTradeSchemaUsesBoolSoftDelete(t *testing.T) {
	sql := AllSQL()
	if strings.Contains(sql, "c_is_deleted TEXT") {
		t.Fatalf("trade schema must not store c_is_deleted as TEXT")
	}
	for _, line := range strings.Split(sql, "\n") {
		if strings.Contains(line, "c_is_deleted") &&
			(strings.Contains(line, "DEFAULT 'false'") || strings.Contains(line, "DEFAULT 'true'")) {
			t.Fatalf("trade schema must not use string defaults for c_is_deleted")
		}
	}
	if got := strings.Count(sql, "c_is_deleted INTEGER NOT NULL DEFAULT 0"); got != 6 {
		t.Fatalf("expected 6 bool soft-delete columns, got %d", got)
	}
}
