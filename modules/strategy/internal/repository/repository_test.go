package repository

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestCommitLogicalRetryIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := New(db)
	if err := db.Create(&domain.State{BindingID: "b", StrategyVersion: "1.0.0", StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	base := domain.Task{BindingID: "b", Version: "1.0.0", Namespace: "default", TriggerBarTime: "t", PreviousState: domain.State{Revision: 0}}
	out := domain.Output{Action: domain.ActionHold, NextState: map[string]any{}}
	if err := r.Commit(context.Background(), base, out, "same"); err != nil {
		t.Fatal(err)
	}
	base.RunID = "retry"
	if err := r.Commit(context.Background(), base, out, "same"); err != nil {
		t.Fatalf("retry should be idempotent: %v", err)
	}
	if err := r.Commit(context.Background(), base, out, "different"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestCommitConcurrentSameLogicalKeyHasOneRun(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := New(db)
	if err := db.Create(&domain.State{BindingID: "b", StrategyVersion: "1.0.0", StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	out := domain.Output{Action: domain.ActionHold, NextState: map[string]any{}}
	base := domain.Task{BindingID: "b", Version: "1.0.0", Namespace: "default", TriggerBarTime: "t", PreviousState: domain.State{Revision: 0}}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			task := base
			task.RunID = runID
			errs <- r.Commit(context.Background(), task, out, "same")
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent logical retry failed: %v", err)
		}
	}
	var count int64
	if err := db.Table("t_strategy_runs").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("runs=%d", count)
	}
}
