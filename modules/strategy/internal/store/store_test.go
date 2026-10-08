package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strategy.sqlite")
	repo, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	return repo
}

var (
	testNow  = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	testHash = "sha256:def"
)

func ptr[T any](value T) *T { return &value }

// seedEnabledInstance 建立定义、实例与会话，并把实例启用到该会话。
func seedEnabledInstance(t *testing.T, repo *Store, instanceID, sessionID string, account *string) {
	t.Helper()
	ctx := context.Background()
	if _, err := repo.GetDefinition(ctx, "s1"); err != nil {
		if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateInstance(ctx, Instance{InstanceID: instanceID, StrategyID: "s1", SpaceID: "space", ViewID: "view_a", LogicalAccountID: account, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := repo.OpenSession(ctx, Session{SessionID: sessionID, InstanceID: instanceID, DSLHash: testHash, ResolvedJSON: `{"view_id":"view_a"}`, CreatedAt: testNow}, "name: demo"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, instanceID, true, &sessionID, json.RawMessage(`{"view_id":"view_a"}`), testNow); err != nil {
		t.Fatal(err)
	}
}

func TestOpenApplySchemaAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.sqlite")
	repo, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsOldStrategyTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.sqlite")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE t_strategies (strategy_id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "停止消费者并备份数据库") {
		t.Fatalf("旧库应被拒绝：%v", err)
	}
}

func TestApplySchemaRejectsMissingRequiredIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	withoutIndex := strings.Replace(schema.AllSQL(), "CREATE INDEX IF NOT EXISTS idx_t_strategy_results_pending\nON t_strategy_results (c_ctime, c_result_id)\nWHERE c_publish_status = 'pending';", "", 1)
	if withoutIndex == schema.AllSQL() {
		t.Fatal("测试未能移除索引语句")
	}
	if err := db.Exec(withoutIndex).Error; err != nil {
		t.Fatal(err)
	}
	if err := New(db).validateCurrentSchema(); err == nil {
		t.Fatal("缺少必需索引的 schema 应被拒绝")
	}
}
