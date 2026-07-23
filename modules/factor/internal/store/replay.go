package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReplayTaskRunning   = "running"
	ReplayTaskSucceeded = "succeeded"
	ReplayTaskFailed    = "failed"
	replayTaskLease     = 15 * time.Minute
)

type replayTaskRecord struct {
	TaskID     string    `gorm:"column:c_task_id;primaryKey"`
	TargetRun  string    `gorm:"column:c_target_run_id;not null"`
	Status     string    `gorm:"column:c_status;not null"`
	Error      string    `gorm:"column:c_error;not null"`
	CreateTime time.Time `gorm:"column:c_ctime;not null"`
	ModifyTime time.Time `gorm:"column:c_mtime;not null"`
}

func (replayTaskRecord) TableName() string { return "t_factor_replay_tasks" }

// ClaimReplayTask reserves a deterministic replay task. A succeeded task is
// never executed again; a failed task may be retried explicitly.
func (s *Store) ClaimReplayTask(ctx context.Context, taskID, targetRunID string) (claimed, alreadySucceeded bool, err error) {
	if s == nil || s.db == nil {
		return false, false, gorm.ErrInvalidDB
	}
	taskID = strings.TrimSpace(taskID)
	targetRunID = strings.TrimSpace(targetRunID)
	if taskID == "" || targetRunID == "" {
		return false, false, errors.New("replay task_id and target_run_id are required")
	}
	now := time.Now().UTC()
	insert := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&replayTaskRecord{
		TaskID: taskID, TargetRun: targetRunID, Status: ReplayTaskRunning, CreateTime: now, ModifyTime: now,
	})
	if insert.Error != nil {
		return false, false, insert.Error
	}
	if insert.RowsAffected == 1 {
		return true, false, nil
	}
	var existing replayTaskRecord
	if err := s.db.WithContext(ctx).Where("c_task_id = ?", taskID).First(&existing).Error; err != nil {
		return false, false, err
	}
	if existing.TargetRun != targetRunID {
		return false, false, errors.New("replay task_id already belongs to another target_run_id")
	}
	if existing.Status == ReplayTaskSucceeded {
		return false, true, nil
	}
	if existing.Status == ReplayTaskRunning && existing.ModifyTime.After(now.Add(-replayTaskLease)) {
		return false, false, nil
	}
	result := s.db.WithContext(ctx).Model(&replayTaskRecord{}).Where("c_task_id = ? AND c_status IN ?", taskID, []string{ReplayTaskFailed, ReplayTaskRunning}).
		Updates(map[string]any{"c_status": ReplayTaskRunning, "c_error": "", "c_mtime": now})
	return result.RowsAffected == 1, false, result.Error
}

func (s *Store) MarkReplayTask(ctx context.Context, taskID, status, errMsg string) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	if status != ReplayTaskSucceeded && status != ReplayTaskFailed {
		return errors.New("invalid replay task status")
	}
	return s.db.WithContext(ctx).Model(&replayTaskRecord{}).Where("c_task_id = ?", strings.TrimSpace(taskID)).
		Updates(map[string]any{"c_status": status, "c_error": errMsg, "c_mtime": time.Now().UTC()}).Error
}
