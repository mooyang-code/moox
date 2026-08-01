package store

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FetchRetryRepository struct{ db *gorm.DB }

func NewFetchRetryRepository(db *gorm.DB) *FetchRetryRepository { return &FetchRetryRepository{db: db} }

func (r *FetchRetryRepository) Upsert(ctx context.Context, item *domain.RetryItem) error {
	if item == nil {
		return gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	if item.CreateTime.IsZero() {
		item.CreateTime = now
	}
	item.ModifyTime = now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_retry_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"c_source_batch_id":    clause.Expr{SQL: "excluded.c_source_batch_id"},
			"c_batch_kind":         clause.Expr{SQL: "excluded.c_batch_kind"},
			"c_attempt":            clause.Expr{SQL: "excluded.c_attempt"},
			"c_status":             clause.Expr{SQL: "excluded.c_status"},
			"c_next_retry_at":      clause.Expr{SQL: "excluded.c_next_retry_at"},
			"c_last_error_type":    clause.Expr{SQL: "excluded.c_last_error_type"},
			"c_last_error_summary": clause.Expr{SQL: "excluded.c_last_error_summary"},
			"c_mtime":              clause.Expr{SQL: "excluded.c_mtime"},
		}),
	}).Create(item).Error
}

func (r *FetchRetryRepository) ListDue(ctx context.Context, spaceID string, now time.Time, limit int) ([]domain.RetryItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var items []domain.RetryItem
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status = ? AND c_next_retry_at IS NOT NULL AND c_next_retry_at <= ?", spaceID, "pending", now.UTC()).
		Order("c_next_retry_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *FetchRetryRepository) Get(ctx context.Context, spaceID, retryKey string) (*domain.RetryItem, error) {
	var item domain.RetryItem
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *FetchRetryRepository) CountPending(ctx context.Context, spaceID, datasetID, frequency string) (int64, error) {
	var count int64
	query := r.db.WithContext(ctx).Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_status = ?", spaceID, "pending")
	if datasetID != "" {
		query = query.Where("c_dataset_id = ?", datasetID)
	}
	if frequency != "" {
		query = query.Where("c_frequency = ?", frequency)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *FetchRetryRepository) MarkStatus(ctx context.Context, spaceID, retryKey, status string) error {
	return r.db.WithContext(ctx).Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).
		Updates(map[string]any{"c_status": status, "c_mtime": time.Now().UTC()}).Error
}

func (r *FetchRetryRepository) Cleanup(ctx context.Context, before time.Time) error {
	return r.db.WithContext(ctx).Where("c_mtime < ?", before.UTC()).Delete(&domain.RetryItem{}).Error
}
