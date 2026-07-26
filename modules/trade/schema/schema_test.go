package schema

import (
	"github.com/stretchr/testify/assert"
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
	if got := strings.Count(sql, "c_is_deleted INTEGER NOT NULL DEFAULT 0"); got != 4 {
		t.Fatalf("expected 4 bool soft-delete columns, got %d", got)
	}
}

func TestAllSQLIncludesTradeSyncCursorSchema(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_trade_sync_cursors",
		"idx_trade_sync_cursors_unique",
		"idx_trade_sync_cursors_account",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
}

func TestAllSQL_ContainsKernelTables(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"t_trade_order_aggregates",
		"t_ledger_entries",
		"t_trade_inbox",
		"t_trade_command_offsets",
		"t_rebalance_runs",
	} {
		assert.Contains(t, sql, want)
	}
}
