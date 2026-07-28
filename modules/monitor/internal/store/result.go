package store

import (
	"context"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (r *ResultRepository) InsertIfAbsent(ctx context.Context, result *domain.CheckResult) (bool, error) {
	tx := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_result_id"}}, DoNothing: true}).
		Create(result)
	return tx.RowsAffected == 1, tx.Error
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

// Latest returns the newest result for each monitored check.
func (r *ResultRepository) Latest(ctx context.Context, limit int) ([]domain.CheckResult, error) {
	if limit <= 0 {
		limit = 500
	}
	var results []domain.CheckResult
	err := r.db.WithContext(ctx).
		Raw(`SELECT r.* FROM t_monitor_checks c
			JOIN t_monitor_check_results r ON r.c_id = (
				SELECT latest.c_id FROM t_monitor_check_results latest
				WHERE latest.c_space_id = c.c_space_id AND latest.c_check_id = c.c_check_id
				ORDER BY latest.c_checked_at DESC, latest.c_id DESC LIMIT 1
			)
			WHERE c.c_enabled = 1
			ORDER BY c.c_space_id ASC, c.c_check_id ASC
			LIMIT ?`, limit).
		Scan(&results).Error
	return results, err
}

func (r *ResultRepository) Stats(ctx context.Context, spaceID string, since time.Time) (float64, int64, error) {
	query := r.db.WithContext(ctx).
		Table("t_monitor_check_results AS result").
		Select("result.*").
		Joins(`JOIN t_monitor_checks AS check_config
			ON check_config.c_space_id = result.c_space_id
			AND check_config.c_check_id = result.c_check_id
			AND check_config.c_enabled = 1`).
		Where("result.c_checked_at >= ?", since)
	if spaceID != "" {
		query = query.Where("result.c_space_id = ?", spaceID)
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
