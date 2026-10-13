package store

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestAgePassedSaveLoadAndIndexSwitch(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.SaveAgePassed(ctx, "space", "view_a", "idx_a", 240, []string{"BTC", "ETH"}, testNow); err != nil {
		t.Fatal(err)
	}
	// 重复登记不报错，也不产生重复行。
	if err := repo.SaveAgePassed(ctx, "space", "view_a", "idx_a", 240, []string{"ETH", "SOL"}, testNow); err != nil {
		t.Fatal(err)
	}
	got, err := repo.LoadAgePassed(ctx, "space", "view_a", "idx_a", 240)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"BTC", "ETH", "SOL"}) {
		t.Fatalf("已通过的标的 = %v", got)
	}
	// 不同的 min_age_bars 互不影响。
	if other, _ := repo.LoadAgePassed(ctx, "space", "view_a", "idx_a", 100); len(other) != 0 {
		t.Fatalf("其他 min_age_bars 不应读到：%v", other)
	}
	// 同一 View 换了活动索引：旧索引的记录在新索引首次登记时清掉；别的 View 不受影响。
	if err := repo.SaveAgePassed(ctx, "space", "view_b", "idx_x", 240, []string{"BTC"}, testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveAgePassed(ctx, "space", "view_a", "idx_b", 240, []string{"BTC"}, testNow); err != nil {
		t.Fatal(err)
	}
	if old, _ := repo.LoadAgePassed(ctx, "space", "view_a", "idx_a", 240); len(old) != 0 {
		t.Fatalf("旧活动索引的记录应已清掉：%v", old)
	}
	if kept, _ := repo.LoadAgePassed(ctx, "space", "view_b", "idx_x", 240); len(kept) != 1 {
		t.Fatalf("其他 View 的记录不应被清：%v", kept)
	}
}

// 已有数据库缺少当前 schema 新增的表时，Open 放行，ApplySchema 补建；已存在的表必须与当前定义一致。
func TestOpenAddsMissingTablesToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.sqlite")
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	if err := raw.Exec(`DROP TABLE t_strategy_age_passed`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := raw.DB()
	_ = sqlDB.Close()

	repo, err := Open(path)
	if err != nil {
		t.Fatalf("缺少新增的表时应能打开：%v", err)
	}
	defer func() { _ = repo.Close() }()
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("补建新增的表：%v", err)
	}
	if err := repo.SaveAgePassed(context.Background(), "space", "view_a", "idx_a", 240, []string{"BTC"}, testNow); err != nil {
		t.Fatal(err)
	}
}
