package repository

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
					"c_factor_id":      binding.FactorID,
					"c_space_id":       binding.SpaceID,
					"c_source_dataset": binding.SourceDataset,
					"c_freq":           binding.Freq,
					"c_subject_mode":   binding.SubjectMode,
					"c_subjects_json":  binding.SubjectsJSON,
					"c_target_dataset": binding.TargetDataset,
					"c_status":         binding.Status,
					"c_mtime":          binding.ModifyTime,
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
			{Name: "c_source_dataset"},
			{Name: "c_freq"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_binding_id",
			"c_subject_mode",
			"c_subjects_json",
			"c_target_dataset",
			"c_status",
			"c_mtime",
		}),
	}).Create(&binding).Error
}

// ListEnabledBySource returns enabled bindings for one source dataset/frequency.
func (r *BindingRepository) ListEnabledBySource(ctx context.Context, spaceID string, sourceDataset string, freq string) ([]domain.FactorBinding, error) {
	var rows []domain.FactorBinding
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_source_dataset = ? AND c_freq = ? AND c_status = ?",
			strings.TrimSpace(spaceID),
			strings.TrimSpace(sourceDataset),
			strings.TrimSpace(freq),
			domain.BindingStatusEnabled,
		).
		Order("c_factor_id ASC").
		Find(&rows).Error
	return rows, err
}

func normalizeBinding(binding *domain.FactorBinding) {
	binding.BindingID = strings.TrimSpace(binding.BindingID)
	binding.FactorID = strings.TrimSpace(binding.FactorID)
	binding.SpaceID = strings.TrimSpace(binding.SpaceID)
	binding.SourceDataset = strings.TrimSpace(binding.SourceDataset)
	binding.Freq = strings.TrimSpace(binding.Freq)
	if binding.SubjectMode == "" {
		binding.SubjectMode = domain.SubjectModeAll
	}
	if binding.SubjectsJSON == "" {
		binding.SubjectsJSON = domain.DefaultSubjectsJSON
	}
	if binding.Status == "" {
		binding.Status = domain.BindingStatusEnabled
	}
}
