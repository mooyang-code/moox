package jobhistory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"gorm.io/gorm"
)

func TestStoreWritesTerminalStateToDayDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStore(StoreOptions{Dir: dir, RetentionDays: 2})
	finished := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	executeAt := finished.Add(-2 * time.Minute)
	state := jobstate.State{
		SpaceID:       "crypto",
		JobID:         "job-1",
		JobItemID:     "ji-1",
		JobType:       "collector.kline",
		ExecuteAt:     &executeAt,
		Status:        jobstate.StatusSuccess,
		ResultSummary: map[string]any{"rows": float64(3)},
		CreatedAt:     finished.Add(-time.Minute),
		UpdatedAt:     finished,
		FinishedAt:    &finished,
		DurationMS:    1000,
		ExecutionNode: "node-1",
	}
	if err := store.WriteTerminal(ctx, state); err != nil {
		t.Fatalf("WriteTerminal() error = %v", err)
	}

	dbPath := filepath.Join(dir, "20260707.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("day db missing: %v", err)
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open day db: %v", err)
	}
	var itemCount int64
	if err := db.Table("t_cloud_job_items").Where("c_space_id = ? AND c_job_item_id = ?", "crypto", "ji-1").Count(&itemCount).Error; err != nil {
		t.Fatalf("count item: %v", err)
	}
	if itemCount != 1 {
		t.Fatalf("itemCount = %d, want 1", itemCount)
	}
	var stored struct {
		ExecuteAt *time.Time `gorm:"column:c_execute_at"`
	}
	if err := db.Table("t_cloud_job_items").
		Select("c_execute_at").
		Where("c_space_id = ? AND c_job_item_id = ?", "crypto", "ji-1").
		Take(&stored).Error; err != nil {
		t.Fatalf("read execute_at: %v", err)
	}
	if stored.ExecuteAt == nil || !stored.ExecuteAt.Equal(executeAt) {
		t.Fatalf("execute_at = %v, want %v", stored.ExecuteAt, executeAt)
	}
	if db.Migrator().HasTable("t_cloud_job_item_attempts") {
		t.Fatal("attempt history table must not exist")
	}
}
