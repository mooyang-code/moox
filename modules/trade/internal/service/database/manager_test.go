package database

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trade.db")
	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{Path: dbPath}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	defer mgr.Close()

	var count int64
	if err := mgr.GetDB().Raw(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'table'
  AND (name LIKE 't_trade_%' OR name IN ('t_accounts', 't_orders', 't_trades', 't_positions'))
`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Initialize() created %d trade tables, want 0", count)
	}
}
