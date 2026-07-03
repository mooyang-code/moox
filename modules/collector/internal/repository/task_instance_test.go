package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
)

func TestTaskInstanceRepositoryUpdateStatusReturnsNotFoundWhenTaskMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&domain.TaskInstance{}); err != nil {
		t.Fatalf("migrate task instances: %v", err)
	}

	repo := NewTaskInstanceRepository(db)
	err = repo.UpdateStatus(context.Background(), "crypto", "missing-task", "node-a", domain.InstanceStatusSuccess, "{}")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("UpdateStatus error = %v, want gorm.ErrRecordNotFound", err)
	}
}
