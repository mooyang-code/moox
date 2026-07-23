package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/schema"
)

func TestReplayTaskClaimIsDurablyIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	claimed, done, err := db.ClaimReplayTask(ctx, "task-1", "run-1")
	if err != nil || !claimed || done {
		t.Fatalf("first claim = claimed:%v done:%v err:%v", claimed, done, err)
	}
	claimed, done, err = db.ClaimReplayTask(ctx, "task-1", "run-1")
	if err != nil || claimed || done {
		t.Fatalf("running duplicate claim = claimed:%v done:%v err:%v", claimed, done, err)
	}
	if err := db.MarkReplayTask(ctx, "task-1", ReplayTaskSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	claimed, done, err = db.ClaimReplayTask(ctx, "task-1", "run-1")
	if err != nil || claimed || !done {
		t.Fatalf("completed duplicate claim = claimed:%v done:%v err:%v", claimed, done, err)
	}
}

func TestReplayTaskClaimRecoversExpiredRunningLease(t *testing.T) {
	ctx := context.Background()
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	claimed, done, err := db.ClaimReplayTask(ctx, "task-expired", "run-1")
	if err != nil || !claimed {
		t.Fatalf("initial claim = %v, %v", claimed, err)
	}
	if err := db.db.Model(&replayTaskRecord{}).Where("c_task_id = ?", "task-expired").Updates(map[string]any{"c_mtime": time.Now().UTC().Add(-replayTaskLease - time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	claimed, done, err = db.ClaimReplayTask(ctx, "task-expired", "run-1")
	if err != nil || !claimed || done {
		t.Fatalf("expired claim = claimed:%v done:%v err:%v", claimed, done, err)
	}
}
