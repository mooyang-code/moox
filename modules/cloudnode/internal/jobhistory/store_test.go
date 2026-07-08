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
	state := jobstate.State{
		SpaceID:       "crypto",
		JobID:         "job-1",
		JobItemID:     "ji-1",
		JobType:       "collector.kline",
		CodePackageID: "pkg-1",
		Status:        jobstate.StatusSuccess,
		ResultSummary: map[string]any{"rows": float64(3)},
		CreatedAt:     finished.Add(-time.Minute),
		UpdatedAt:     finished,
		FinishedAt:    &finished,
		Attempts: []jobstate.Attempt{{
			AttemptNo:  1,
			NodeID:     "node-1",
			Status:     jobstate.AttemptSuccess,
			StartedAt:  finished.Add(-time.Second),
			FinishedAt: &finished,
		}},
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
	var attemptCount int64
	if err := db.Table("t_cloud_job_item_attempts").Where("c_space_id = ? AND c_job_item_id = ?", "crypto", "ji-1").Count(&attemptCount).Error; err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Fatalf("attemptCount = %d, want 1", attemptCount)
	}
}
