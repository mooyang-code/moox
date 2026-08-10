package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BindingRepository persists factor bindings.
type BindingRepository struct {
	db *gorm.DB
}

// BindingFilter describes a paginated binding query.
type BindingFilter struct {
	SpaceID      string
	SourceViewID string
	Freq         string
	Status       string
	Page         Page
}

// NewBindingRepository creates a binding repository.
func NewBindingRepository(db *gorm.DB) *BindingRepository {
	return &BindingRepository{db: db}
}

// Upsert inserts or updates a binding by its natural scope key.
func (r *BindingRepository) Upsert(ctx context.Context, binding domain.FactorBinding) error {
	now := time.Now().UTC()
	normalizeBinding(&binding)
	if binding.CreateTime.IsZero() {
		binding.CreateTime = now
	}
	binding.ModifyTime = now
	if binding.BindingID != "" {
		var existing domain.FactorBinding
		err := r.db.WithContext(ctx).Where("c_binding_id = ?", binding.BindingID).First(&existing).Error
		if err == nil {
			return r.db.WithContext(ctx).Model(&domain.FactorBinding{}).
				Where("c_binding_id = ?", binding.BindingID).
				Updates(map[string]any{
					"c_factor_id":         binding.FactorID,
					"c_space_id":          binding.SpaceID,
					"c_source_view_id":    binding.SourceViewID,
					"c_freq":              binding.Freq,
					"c_subject_mode":      binding.SubjectMode,
					"c_subjects_json":     binding.SubjectsJSON,
					"c_result_dataset_id": binding.ResultDatasetID,
					"c_result_view_id":    binding.ResultViewID,
					"c_status":            binding.Status,
					"c_mtime":             binding.ModifyTime,
				}).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_factor_id"},
			{Name: "c_space_id"},
			{Name: "c_source_view_id"},
			{Name: "c_freq"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_binding_id",
			"c_subject_mode",
			"c_subjects_json",
			"c_result_dataset_id",
			"c_result_view_id",
			"c_status",
			"c_mtime",
		}),
	}).Create(&binding).Error
}

// ListExecutable returns bindings whose binding and factor are both enabled.
func (r *BindingRepository) ListExecutable(ctx context.Context) ([]domain.FactorBinding, error) {
	var rows []domain.FactorBinding
	err := r.db.WithContext(ctx).
		Table("t_factor_bindings AS b").
		Select("b.*").
		Joins("JOIN t_factor_defs AS f ON f.c_factor_id = b.c_factor_id").
		Where("b.c_status = ? AND f.c_status = ?",
			domain.BindingStatusEnabled,
			domain.FactorStatusEnabled,
		).
		Order("b.c_space_id, b.c_source_view_id, b.c_freq, b.c_factor_id").
		Scan(&rows).Error
	hydrateLegacyBindings(rows)
	return rows, err
}

// HasExecutableOrPending reports whether an enabled factor still has work
// waiting for this source View. It closes the small window where a source-ready
// marker can arrive while a pending binding is being promoted by the
// reconciler: the marker must be retried, not acknowledged and lost.
func (r *BindingRepository) HasExecutableOrPending(ctx context.Context, spaceID, sourceViewID, freq string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Table("t_factor_bindings AS b").
		Joins("JOIN t_factor_defs AS f ON f.c_factor_id = b.c_factor_id").
		Where("b.c_space_id = ? AND b.c_source_view_id = ? AND b.c_freq = ?", spaceID, sourceViewID, freq).
		Where("b.c_status IN ? AND f.c_status = ?", []string{domain.BindingStatusEnabled, domain.BindingStatusPendingView}, domain.FactorStatusEnabled).
		Count(&count).Error
	return count > 0, err
}

// ListByFactor returns all bindings for one factor.
func (r *BindingRepository) ListByFactor(ctx context.Context, factorID string) ([]domain.FactorBinding, error) {
	var rows []domain.FactorBinding
	err := r.db.WithContext(ctx).
		Where("c_factor_id = ?", strings.TrimSpace(factorID)).
		Order("c_space_id ASC, c_source_view_id ASC, c_freq ASC").
		Find(&rows).Error
	hydrateLegacyBindings(rows)
	return rows, err
}

// List returns bindings matching the filter.
func (r *BindingRepository) List(ctx context.Context, filter BindingFilter) ([]domain.FactorBinding, int64, error) {
	page, size := normalizePage(filter.Page)
	q := r.db.WithContext(ctx).Model(&domain.FactorBinding{})
	if v := strings.TrimSpace(filter.SpaceID); v != "" {
		q = q.Where("c_space_id = ?", v)
	}
	if v := strings.TrimSpace(filter.SourceViewID); v != "" {
		q = q.Where("c_source_view_id = ?", v)
	}
	if v := strings.TrimSpace(filter.Freq); v != "" {
		q = q.Where("c_freq = ?", v)
	}
	if v := strings.TrimSpace(filter.Status); v != "" {
		q = q.Where("c_status = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []domain.FactorBinding
	if err := q.Order("c_mtime DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	hydrateLegacyBindings(rows)
	return rows, total, nil
}

func hydrateLegacyBindings(rows []domain.FactorBinding) {
	for i := range rows {
		rows[i].SourceDataset = rows[i].SourceViewID
		rows[i].TargetDataset = rows[i].ResultDatasetID
	}
}

// Delete removes a binding by ID.
func (r *BindingRepository) Delete(ctx context.Context, bindingID string) error {
	result := r.db.WithContext(ctx).Where("c_binding_id = ?", strings.TrimSpace(bindingID)).Delete(&domain.FactorBinding{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func normalizeBinding(binding *domain.FactorBinding) {
	if binding.SourceViewID == "" {
		binding.SourceViewID = binding.SourceDataset
	}
	if binding.ResultDatasetID == "" && binding.TargetDataset != domain.DefaultBindingTargetID {
		binding.ResultDatasetID = binding.TargetDataset
	}
	binding.BindingID = strings.TrimSpace(binding.BindingID)
	binding.FactorID = strings.TrimSpace(binding.FactorID)
	binding.SpaceID = strings.TrimSpace(binding.SpaceID)
	binding.SourceViewID = strings.TrimSpace(binding.SourceViewID)
	binding.ResultDatasetID = strings.TrimSpace(binding.ResultDatasetID)
	binding.ResultViewID = strings.TrimSpace(binding.ResultViewID)
	binding.SourceDataset = binding.SourceViewID
	binding.TargetDataset = binding.ResultDatasetID
	binding.Freq = strings.TrimSpace(binding.Freq)
	if binding.SubjectMode == "" {
		binding.SubjectMode = domain.SubjectModeAll
	}
	if binding.SubjectsJSON == "" {
		binding.SubjectsJSON = domain.DefaultSubjectsJSON
	}
	if binding.Status == "" {
		binding.Status = domain.BindingStatusPendingView
	}
}
