package repository

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

// NewFactorRepository creates a factor repository.
func NewFactorRepository(db *gorm.DB) *FactorRepository {
	return &FactorRepository{db: db}
}

// Upsert inserts or updates a factor by factor_id.
func (r *FactorRepository) Upsert(ctx context.Context, factor domain.FactorDef) error {
	now := time.Now().UTC()
	normalizeFactor(&factor)
	if factor.CreateTime.IsZero() {
		factor.CreateTime = now
	}
	factor.ModifyTime = now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_factor_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_name",
			"c_kind",
			"c_source_code",
			"c_source_hash",
			"c_params_json",
			"c_lookback_bars",
			"c_writeback_bars",
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

// ListEnabledTimeseries returns enabled V1 time-series factor definitions.
func (r *FactorRepository) ListEnabledTimeseries(ctx context.Context) ([]domain.FactorDef, error) {
	var rows []domain.FactorDef
	err := r.db.WithContext(ctx).
		Where("c_kind = ? AND c_status = ?", domain.FactorKindTimeseries, domain.FactorStatusEnabled).
		Order("c_name ASC, c_factor_id ASC").
		Find(&rows).Error
	return rows, err
}

func normalizeFactor(factor *domain.FactorDef) {
	factor.FactorID = strings.TrimSpace(factor.FactorID)
	factor.Name = strings.TrimSpace(factor.Name)
	if factor.Kind == "" {
		factor.Kind = domain.FactorKindTimeseries
	}
	if factor.ParamsJSON == "" {
		factor.ParamsJSON = domain.DefaultFactorParamsJSON
	}
	if factor.DependsJSON == "" {
		factor.DependsJSON = domain.DefaultFactorDependsJSON
	}
	if factor.Status == "" {
		factor.Status = domain.FactorStatusDisabled
	}
	if factor.WritebackBars == 0 {
		factor.WritebackBars = 5
	}
}
