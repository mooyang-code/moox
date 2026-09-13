package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type subjectRun struct {
	PeriodTime int64     `gorm:"column:c_period_time"`
	TaskID     string    `gorm:"column:c_task_id;primaryKey"`
	ScopeKey   string    `gorm:"column:c_scope_key"`
	TaskJSON   string    `gorm:"column:c_task_json"`
	Status     string    `gorm:"column:c_status"`
	Error      string    `gorm:"column:c_error"`
	UpdatedAt  time.Time `gorm:"column:c_updated_at"`
}

func (subjectRun) TableName() string { return "t_factor_subject_runs" }

type subjectHead struct {
	PeriodTime      int64  `gorm:"column:c_period_time"`
	ScopeKey        string `gorm:"column:c_scope_key;primaryKey"`
	TaskID          string `gorm:"column:c_task_id"`
	SourceNode      string `gorm:"column:c_source_node"`
	SourceStore     string `gorm:"column:c_source_store"`
	SourceSequence  string `gorm:"column:c_source_sequence"`
	SourceEvent     string `gorm:"column:c_source_event"`
	CatalogRevision int64  `gorm:"column:c_catalog_revision"`
}

func (subjectHead) TableName() string { return "t_factor_subject_heads" }

func subjectTaskScope(task engine.FactorTask) (string, error) {
	if task.TaskID == "" || task.BindingID == "" || task.BindingGeneration == "" || task.SpaceID == "" || task.SourceViewID == "" || task.SubjectID == "" || task.Freq == "" || task.PeriodTime <= 0 || task.InputContractVersion == "" || !task.FilterSourceSeriesTag || task.CatalogRevision <= 0 || task.SourceNodeID == "" || task.SourceStoreID == "" || task.SourceSequence == 0 || task.SourceEventID == "" {
		return "", fmt.Errorf("subject task provenance is incomplete")
	}
	raw, err := json.Marshal([]any{task.BindingID, task.BindingGeneration, task.SpaceID, task.SourceViewID, task.InputContractVersion, task.SubjectID, task.Freq, task.PeriodTime, task.SourceSeriesTag})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

// AdmitSubjectTask persists the newest admitted source position before writes.
// The single engine must hold its shared operation gate until completion;
// this is durable replay fencing, not a distributed execution lease.
func (s *Store) AdmitSubjectTask(ctx context.Context, task engine.FactorTask) (bool, error) {
	scope, err := subjectTaskScope(task)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return false, err
	}
	run := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cutoff int64
		if err := tx.Raw("SELECT c_completed_before FROM t_factor_subject_gc WHERE c_id = 1").Scan(&cutoff).Error; err != nil {
			return err
		}
		if task.PeriodTime < cutoff {
			return nil
		}
		var head subjectHead
		err := tx.Where("c_scope_key = ?", scope).Take(&head).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if head.SourceNode != task.SourceNodeID || head.SourceStore != task.SourceStoreID {
				return fmt.Errorf("subject source incarnation is incomparable; a new input contract is required")
			}
			sequence, err := strconv.ParseUint(head.SourceSequence, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid persisted source sequence: %w", err)
			}
			if task.SourceSequence < sequence || task.CatalogRevision < head.CatalogRevision {
				return nil
			}
			if task.SourceSequence == sequence && task.SourceEventID != head.SourceEvent {
				return fmt.Errorf("source sequence was reused for a different event")
			}
		}
		var previous subjectRun
		lookup := tx.Where("c_task_id = ?", task.TaskID).Take(&previous).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if lookup == nil {
			var pinned engine.FactorTask
			if err := json.Unmarshal([]byte(previous.TaskJSON), &pinned); err != nil {
				return err
			}
			if previous.ScopeKey != scope || pinned.SourceNodeID != task.SourceNodeID || pinned.SourceStoreID != task.SourceStoreID || pinned.SourceSequence != task.SourceSequence || pinned.SourceEventID != task.SourceEventID {
				return fmt.Errorf("subject task identity was reused")
			}
			if previous.Status == "complete" && head.TaskID == task.TaskID {
				return tx.Model(&subjectHead{}).Where("c_scope_key = ?", scope).Update("c_catalog_revision", task.CatalogRevision).Error
			}
		}
		if head.TaskID != "" && head.TaskID != task.TaskID {
			if err := tx.Model(&subjectRun{}).Where("c_task_id = ? AND c_status IN ?", head.TaskID, []string{"pending", "failed"}).Updates(map[string]any{"c_status": "superseded", "c_updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		head = subjectHead{PeriodTime: task.PeriodTime, ScopeKey: scope, TaskID: task.TaskID, SourceNode: task.SourceNodeID, SourceStore: task.SourceStoreID, SourceSequence: strconv.FormatUint(task.SourceSequence, 10), SourceEvent: task.SourceEventID, CatalogRevision: task.CatalogRevision}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&head).Error; err != nil {
			return err
		}
		row := subjectRun{PeriodTime: task.PeriodTime, TaskID: task.TaskID, ScopeKey: scope, TaskJSON: string(raw), Status: "pending", UpdatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
		run = true
		return nil
	})
	return run, err
}

// PruneSubjectRunsBefore accepts a durable completion barrier, never a wall
// clock TTL. The caller must hold the operation gate and prove older periods
// are finalized; corrections below the barrier require explicit recalculation.
// The retained watermark prevents JetStream replays from resurrecting GC'd work.
func (s *Store) PruneSubjectRunsBefore(ctx context.Context, completedBefore int64) error {
	if completedBefore <= 0 {
		return fmt.Errorf("positive subject completion barrier is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var outstanding int64
		if err := tx.Model(&subjectRun{}).Where("c_period_time < ? AND c_status NOT IN ?", completedBefore, []string{"complete", "superseded"}).Count(&outstanding).Error; err != nil {
			return err
		}
		if outstanding != 0 {
			return fmt.Errorf("subject completion barrier contains unfinished tasks")
		}
		if err := tx.Exec("UPDATE t_factor_subject_gc SET c_completed_before = MAX(c_completed_before, ?) WHERE c_id = 1", completedBefore).Error; err != nil {
			return err
		}
		if err := tx.Where("c_period_time < ?", completedBefore).Delete(&subjectRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("c_period_time < ?", completedBefore).Delete(&subjectReceipt{}).Error; err != nil {
			return err
		}
		return tx.Where("c_period_time < ?", completedBefore).Delete(&subjectHead{}).Error
	})
}

func (s *Store) CompleteSubjectTask(ctx context.Context, task engine.FactorTask, failure string) error {
	scope, err := subjectTaskScope(task)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var head subjectHead
		if err := tx.Where("c_scope_key = ?", scope).Take(&head).Error; err != nil {
			return err
		}
		if head.TaskID != task.TaskID || head.SourceNode != task.SourceNodeID || head.SourceStore != task.SourceStoreID || head.SourceSequence != strconv.FormatUint(task.SourceSequence, 10) || head.SourceEvent != task.SourceEventID {
			return fmt.Errorf("subject task is no longer the admitted head")
		}
		status := "complete"
		if failure != "" {
			status = "failed"
		}
		result := tx.Model(&subjectRun{}).Where("c_task_id = ? AND c_scope_key = ? AND c_status = ?", task.TaskID, scope, "pending").Updates(map[string]any{"c_status": status, "c_error": failure, "c_updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("subject task has no pending admission")
		}
		return nil
	})
}
