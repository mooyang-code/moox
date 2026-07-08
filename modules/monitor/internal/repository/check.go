package repository

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

type CheckRepository struct {
	db *gorm.DB
}

type ListChecksOptions struct {
	SpaceID   string
	GroupName string
	Source    string
	Enabled   *bool
	Page      Page
}

func NewCheckRepository(db *gorm.DB) *CheckRepository {
	return &CheckRepository{db: db}
}

func (r *CheckRepository) Create(ctx context.Context, check *domain.Check) error {
	return r.db.WithContext(ctx).Create(check).Error
}

func (r *CheckRepository) Get(ctx context.Context, spaceID, checkID string) (*domain.Check, error) {
	var check domain.Check
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ? AND c_is_deleted = 0", spaceID, checkID).
		First(&check).Error
	if err != nil {
		return nil, err
	}
	return &check, nil
}

func (r *CheckRepository) Update(ctx context.Context, check *domain.Check) error {
	return r.db.WithContext(ctx).
		Model(&domain.Check{}).
		Where("c_space_id = ? AND c_check_id = ? AND c_is_deleted = 0", check.SpaceID, check.CheckID).
		Updates(check).Error
}

func (r *CheckRepository) Delete(ctx context.Context, spaceID, checkID string) error {
	return r.db.WithContext(ctx).
		Model(&domain.Check{}).
		Where("c_space_id = ? AND c_check_id = ? AND c_is_deleted = 0", spaceID, checkID).
		Updates(map[string]any{"c_is_deleted": true}).Error
}

func (r *CheckRepository) List(ctx context.Context, opts ListChecksOptions) ([]domain.Check, error) {
	q := r.db.WithContext(ctx).Where("c_is_deleted = 0")
	if opts.SpaceID != "" {
		q = q.Where("c_space_id = ?", opts.SpaceID)
	}
	if opts.GroupName != "" {
		q = q.Where("c_group_name = ?", opts.GroupName)
	}
	if opts.Source != "" {
		q = q.Where("c_source = ?", opts.Source)
	}
	if opts.Enabled != nil {
		q = q.Where("c_enabled = ?", *opts.Enabled)
	}
	var checks []domain.Check
	err := q.Order("c_group_name ASC, c_name ASC").
		Limit(opts.Page.limit()).
		Offset(opts.Page.offset()).
		Find(&checks).Error
	return checks, err
}

func (r *CheckRepository) ListDue(ctx context.Context, now time.Time, limit int) ([]domain.Check, error) {
	if limit <= 0 {
		limit = 100
	}
	var checks []domain.Check
	err := r.db.WithContext(ctx).
		Where("c_is_deleted = 0 AND c_enabled = 1 AND (c_next_check_at IS NULL OR c_next_check_at <= ?)", now).
		Order("c_next_check_at ASC, c_id ASC").
		Limit(limit).
		Find(&checks).Error
	return checks, err
}
