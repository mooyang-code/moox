package store

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FactorRepository persists factor definitions.
type FactorRepository struct {
	db *gorm.DB
}

// FactorFilter describes a paginated factor query.
type FactorFilter struct {
	Status string
	Page   Page
}

// NewFactorRepository creates a factor repository.
func NewFactorRepository(db *gorm.DB) *FactorRepository {
	return &FactorRepository{db: db}
}

// Upsert inserts or updates a factor by factor_id.
func (r *FactorRepository) Upsert(ctx context.Context, factor domain.FactorDef) error {
	now := time.Now().UTC()
	if factor.Periods == nil {
		factor.Periods = []int{}
	}
	if factor.Depends == nil {
		factor.Depends = []string{}
	}
	if factor.CreateTime.IsZero() {
		factor.CreateTime = now
	}
	factor.ModifyTime = now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_factor_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_name",
			"c_source_code",
			"c_source_hash",
			"c_source_path",
			"c_periods_json",
			"c_lookback_bars",
			"c_depends_json",
			"c_status",
			"c_mtime",
		}),
	}).Create(&factor).Error
}

// Get returns a factor by id.
func (r *FactorRepository) Get(ctx context.Context, factorID string) (*domain.FactorDef, error) {
	var factor domain.FactorDef
	if err := r.db.WithContext(ctx).Where("c_factor_id = ?", strings.TrimSpace(factorID)).First(&factor).Error; err != nil {
		return nil, err
	}
	return &factor, nil
}

func (r *FactorRepository) GetByName(ctx context.Context, name string) (*domain.FactorDef, error) {
	var factor domain.FactorDef
	if err := r.db.WithContext(ctx).Where("c_name = ?", strings.TrimSpace(name)).First(&factor).Error; err != nil {
		return nil, err
	}
	return &factor, nil
}

// List returns factor definitions matching the filter.
func (r *FactorRepository) List(ctx context.Context, filter FactorFilter) ([]domain.FactorDef, int64, error) {
	page, size := normalizePage(filter.Page)
	q := r.db.WithContext(ctx).Model(&domain.FactorDef{})
	if strings.TrimSpace(filter.Status) != "" {
		q = q.Where("c_status = ?", strings.TrimSpace(filter.Status))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []domain.FactorDef
	if err := q.Order("c_mtime DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// SetStatus updates one factor's lifecycle status.
func (r *FactorRepository) SetStatus(ctx context.Context, factorID, status string) error {
	result := r.db.WithContext(ctx).Model(&domain.FactorDef{}).
		Where("c_factor_id = ?", strings.TrimSpace(factorID)).
		Updates(map[string]any{"c_status": strings.TrimSpace(status), "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListEnabled returns enabled time-series factor definitions.
func (r *FactorRepository) ListEnabled(ctx context.Context) ([]domain.FactorDef, error) {
	var rows []domain.FactorDef
	err := r.db.WithContext(ctx).
		Where("c_status = ?", domain.FactorStatusEnabled).
		Order("c_name ASC, c_factor_id ASC").
		Find(&rows).Error
	return rows, err
}
