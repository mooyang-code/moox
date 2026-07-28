package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRunInitCommandAppliesTradeSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trade.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	assertTableExists(t, dbPath, "t_exchange_accounts")
	assertTableExists(t, dbPath, "t_order_fills")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestIsInitCommand_ShouldDetectOnlyInitSubcommand(t *testing.T) {
	if !isInitCommand([]string{"trade", "init"}) {
		t.Fatal("expected init command to be detected")
	}
	if isInitCommand([]string{"trade", "serve"}) {
		t.Fatal("serve must not be detected as init")
	}
	if isInitCommand([]string{"trade"}) {
		t.Fatal("short args must not be detected as init")
	}
}

func TestRunInitCommand_InvalidArgs_ShouldReturnError(t *testing.T) {
	if err := runInitCommand([]string{"serve"}, nil, nil); err == nil {
		t.Fatal("expected command mismatch error")
	}
	var stderr bytes.Buffer
	err := runInitCommand([]string{"init", "extra"}, nil, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unexpected init arguments") {
		t.Fatalf("err=%v, want unexpected args", err)
	}
}

func TestPrintInitError_ShouldWriteJSON(t *testing.T) {
	var stderr bytes.Buffer
	printInitError(&stderr, assertErr("boom"))
	var got map[string]string
	if err := json.Unmarshal(stderr.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] != "init_failed" || got["message"] != "boom" {
		t.Fatalf("payload=%v", got)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func assertTableExists(t *testing.T, dbPath string, tableName string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tableName).Scan(&count).Error; err != nil {
		t.Fatalf("query table %s: %v", tableName, err)
	}
	if count != 1 {
		t.Fatalf("table %s exists = %d, want 1", tableName, count)
	}
}
