package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/ruleseed"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assertTableExists(t, dbPath, "t_collector_resample_jobs")
	assertTableNotExists(t, dbPath, "t_collector_execution_logs")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

// 用随发布安装的真实种子（config/setup/collection-tasks.yaml）初始化：每个内置任务都写进去，采集方式按市场默认值填上
// （crypto 是 stream，A 股、港股、美股是 scf，合成任务为空）；重跑不重复写入。
func TestRunInitCommandSeedsBuiltInTasks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	seedPath := filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml")
	seeds, err := ruleseed.LoadFile(seedPath)
	require.NoError(t, err)
	require.NotEmpty(t, seeds)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	require.NoError(t, runInitCommand([]string{"init", "--db-path", dbPath, "--seed-file", seedPath}, &stdout, &stderr), stderr.String())
	var result initResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, len(seeds), result.TasksCreated)
	assert.Equal(t, 0, result.TasksUnchanged)
	assert.NotContains(t, stdout.String(), `"created"`)
	stdout.Reset()
	require.NoError(t, runInitCommand([]string{"init", "--db-path", dbPath, "--seed-file", seedPath}, &stdout, &stderr), stderr.String())
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, 0, result.TasksCreated)
	assert.Equal(t, len(seeds), result.TasksUnchanged)

	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	defer mgr.Close()
	want := map[string]string{"crypto": domain.IngestStreaming, "stockcn": domain.IngestSCF, "stockhk": domain.IngestSCF, "stockus": domain.IngestSCF}
	for _, seed := range seeds {
		task, err := mgr.Tasks().GetByTaskID(context.Background(), seed.SpaceID, seed.TaskID)
		require.NoError(t, err, seed.TaskID)
		assert.Equal(t, seed.Enabled, task.Enabled, seed.TaskID)
		switch task.DataType {
		case domain.DataTypeKline:
			expected, ok := want[task.SpaceID]
			require.True(t, ok, "种子里出现了没登记期望值的空间 %s", task.SpaceID)
			assert.Equal(t, expected, task.Ingest, "%s/%s", task.SpaceID, task.TaskID)
		default:
			assert.Empty(t, task.Ingest, "合成任务没有采集方式：%s/%s", task.SpaceID, task.TaskID)
		}
	}
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
