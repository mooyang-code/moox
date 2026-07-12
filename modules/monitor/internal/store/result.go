package store

import (
	"context"
	"sort"
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

func (r *ResultRepository) Stats(ctx context.Context, spaceID string, since time.Time) (float64, int64, error) {
	query := r.db.WithContext(ctx).Where("c_checked_at >= ?", since)
	if spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	var results []domain.CheckResult
	if err := query.Find(&results).Error; err != nil {
		return 0, 0, err
	}
	if len(results) == 0 {
		return 0, 0, nil
	}
	successes := 0
	latencies := make([]int64, 0, len(results))
	for _, result := range results {
		if result.Success {
			successes++
			latencies = append(latencies, result.LatencyMS)
		}
	}
	var p95 int64
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p95 = latencies[int(float64(len(latencies)-1)*0.95)]
	}
	return float64(successes) / float64(len(results)), p95, nil
}

func (r *ResultRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).
		Where("c_checked_at < ?", cutoff).
		Delete(&domain.CheckResult{})
	return tx.RowsAffected, tx.Error
}
