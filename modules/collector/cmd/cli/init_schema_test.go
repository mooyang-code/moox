package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestIsInitCommand(t *testing.T) {
	assert.False(t, isInitCommand(nil))
	assert.False(t, isInitCommand([]string{"moox-collector"}))
	assert.True(t, isInitCommand([]string{"moox-collector", "init"}))
}

func TestPrintInitError(t *testing.T) {
	var buf bytes.Buffer
	printInitError(&buf, errors.New("schema failed"))
	assert.Contains(t, buf.String(), "init_failed")
	assert.Contains(t, buf.String(), "schema failed")
	printInitError(nil, errors.New("ignored"))
}

func TestRunInitCommandAppliesCollectorSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	assertTableExists(t, dbPath, "t_collector_task_instances")
	assertTableNotExists(t, dbPath, "t_collector_execution_logs")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestRunInitCommandSeedsBuiltInRules(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	seedPath := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(seedPath, []byte("rules:\n- space_id: crypto\n  rule_id: builtin-rule\n  data_type: instrument\n  provider: binance\n  market_type: spot\n  enabled: true\n  collect_params:\n    provider: binance\n    market_type: spot\n    symbol_source: exchange\n    target_dataset_id: dataset_binance_spot_symbols\n    frequency: 1h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runInitCommand([]string{"init", "--db-path", dbPath, "--seed-file", seedPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	var result initResult
	assert.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, 1, result.RulesCreated)
	assert.Equal(t, 0, result.RulesUnchanged)
	stdout.Reset()
	if err := runInitCommand([]string{"init", "--db-path", dbPath, "--seed-file", seedPath}, &stdout, &stderr); err != nil {
		t.Fatalf("second runInitCommand() error = %v", err)
	}
	assert.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, 0, result.RulesCreated)
	assert.Equal(t, 1, result.RulesUnchanged)
	mgr, err := store.Open(&store.Options{Path: dbPath})
	assert.NoError(t, err)
	defer mgr.Close()
	rule, err := mgr.TaskRules().GetByRuleID(context.Background(), "crypto", "builtin-rule")
	assert.NoError(t, err)
	assert.True(t, rule.Enabled)
}

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

func assertTableNotExists(t *testing.T, dbPath string, tableName string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tableName).Scan(&count).Error; err != nil {
		t.Fatalf("query table %s: %v", tableName, err)
	}
	if count != 0 {
		t.Fatalf("table %s exists = %d, want 0", tableName, count)
	}
}
