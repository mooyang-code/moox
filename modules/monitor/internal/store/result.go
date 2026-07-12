package store

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

type ResultRepository struct {
	db *gorm.DB
}

func NewResultRepository(db *gorm.DB) *ResultRepository {
	return &ResultRepository{db: db}
}

func (r *ResultRepository) Insert(ctx context.Context, result *domain.CheckResult) error {
	return r.db.WithContext(ctx).Create(result).Error
}

func (r *ResultRepository) Recent(ctx context.Context, spaceID, checkID string, limit int) ([]domain.CheckResult, error) {
	if limit <= 0 {
		limit = 50
	}
	var results []domain.CheckResult
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
		Order("c_checked_at DESC").
		Limit(limit).
		Find(&results).Error
	return results, err
}

func (r *ResultRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).
		Where("c_checked_at < ?", cutoff).
		Delete(&domain.CheckResult{})
	return tx.RowsAffected, tx.Error
}
