package repository

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// RunScopeFilter describes execution-log list filters.
type RunScopeFilter struct {
	SpaceID       string
	SourceDataset string
	SubjectID     string
	Freq          string
	Status        string
}

// RunRepository persists terminal factor run records.
type RunRepository struct {
	db *gorm.DB
}

// NewRunRepository creates a run repository.
func NewRunRepository(db *gorm.DB) *RunRepository {
	return &RunRepository{db: db}
}

// Insert appends one terminal run record.
func (r *RunRepository) Insert(ctx context.Context, run domain.FactorRun) error {
	normalizeRun(&run)
	if run.CreateTime.IsZero() {
		run.CreateTime = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Create(&run).Error
}

// ListByScope returns run records ordered by latest covered bar first.
func (r *RunRepository) ListByScope(ctx context.Context, filter RunScopeFilter, page Page) ([]domain.FactorRun, error) {
	q := r.applyScopeFilter(r.db.WithContext(ctx).Model(&domain.FactorRun{}), filter)
	pageNo, size := normalizePage(page)
	var rows []domain.FactorRun
	err := q.Order("c_bar_time DESC").Limit(size).Offset((pageNo - 1) * size).Find(&rows).Error
	return rows, err
}

// CountByScope returns the total run count matching a list filter.
func (r *RunRepository) CountByScope(ctx context.Context, filter RunScopeFilter) (int64, error) {
	q := r.applyScopeFilter(r.db.WithContext(ctx).Model(&domain.FactorRun{}), filter)
	var total int64
	err := q.Count(&total).Error
	return total, err
}

// DeleteRunsBefore deletes old run records by create time.
func (r *RunRepository) DeleteRunsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("c_ctime < ?", cutoff.UTC()).
		Delete(&domain.FactorRun{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *RunRepository) applyScopeFilter(q *gorm.DB, filter RunScopeFilter) *gorm.DB {
	if v := strings.TrimSpace(filter.SpaceID); v != "" {
		q = q.Where("c_space_id = ?", v)
	}
	if v := strings.TrimSpace(filter.SourceDataset); v != "" {
		q = q.Where("c_source_dataset = ?", v)
	}
	if v := strings.TrimSpace(filter.SubjectID); v != "" {
		q = q.Where("c_subject_id = ?", v)
	}
	if v := strings.TrimSpace(filter.Freq); v != "" {
		q = q.Where("c_freq = ?", v)
	}
	if v := strings.TrimSpace(filter.Status); v != "" {
		q = q.Where("c_status = ?", v)
	}
	return q
}

func normalizeRun(run *domain.FactorRun) {
	run.RunID = strings.TrimSpace(run.RunID)
	run.TriggerType = strings.TrimSpace(run.TriggerType)
	run.SpaceID = strings.TrimSpace(run.SpaceID)
	run.SourceDataset = strings.TrimSpace(run.SourceDataset)
	run.TargetDataset = strings.TrimSpace(run.TargetDataset)
	run.SubjectID = strings.TrimSpace(run.SubjectID)
	run.Freq = strings.TrimSpace(run.Freq)
	run.BarTime = strings.TrimSpace(run.BarTime)
	if run.Status == "" {
		run.Status = domain.RunStatusSucceeded
	}
}
