package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
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

// Create inserts a new factor definition without replacing existing rows.
func (r *FactorRepository) Create(ctx context.Context, factor domain.FactorDef) error {
	now := time.Now().UTC()
	if factor.InputColumns == nil {
		factor.InputColumns = []string{}
	}
	if factor.Outputs == nil {
		factor.Outputs = []string{}
	}
	if factor.CreateTime.IsZero() {
		factor.CreateTime = now
	}
	factor.ModifyTime = now
	return r.db.WithContext(ctx).Create(&factor).Error
}

// Update changes definition content. Name, outputs, and lifecycle status are immutable here.
func (r *FactorRepository) Update(ctx context.Context, factor domain.FactorDef) error {
	inputColumnsJSON, err := json.Marshal(factor.InputColumns)
	if err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&domain.FactorDef{}).
		Where("c_factor_id = ?", strings.TrimSpace(factor.FactorID)).
		Updates(map[string]any{
			"c_source_code": factor.SourceCode, "c_source_hash": factor.SourceHash,
			"c_source_path":        factor.SourcePath,
			"c_input_columns_json": string(inputColumnsJSON), "c_params_json": factor.ParamsJSON,
			"c_lookback_periods": factor.LookbackPeriods,
			"c_mtime":            time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
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

// Delete removes one factor definition.
func (r *FactorRepository) Delete(ctx context.Context, factorID string) error {
	result := r.db.WithContext(ctx).
		Where("c_factor_id = ?", strings.TrimSpace(factorID)).
		Delete(&domain.FactorDef{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
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
