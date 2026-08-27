package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OutputManifestKey identifies one binding/subject output period.
type OutputManifestKey struct {
	BindingID  string
	SubjectID  string
	Frequency  string
	PeriodTime time.Time
}

// OutputManifestRepository persists the dynamic RowKeys currently owned by a task.
type OutputManifestRepository struct{ db *gorm.DB }

func NewOutputManifestRepository(db *gorm.DB) *OutputManifestRepository {
	return &OutputManifestRepository{db: db}
}

func (r *OutputManifestRepository) Get(ctx context.Context, key OutputManifestKey) ([]string, error) {
	var row domain.OutputManifest
	result := r.db.WithContext(ctx).Where(
		"c_binding_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_period_time = ?",
		strings.TrimSpace(key.BindingID), strings.TrimSpace(key.SubjectID), strings.TrimSpace(key.Frequency), key.PeriodTime.UTC().UnixNano(),
	).Limit(1).Find(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(row.RowKeysJSON), &keys); err != nil {
		return nil, fmt.Errorf("decode output manifest %s: %w", key.BindingID, err)
	}
	return normalizeManifestKeys(keys), nil
}

// ListByBinding returns manifest keys that still own result rows. It is used
// by the lifecycle cleanup path before a binding is disabled or removed.
func (r *OutputManifestRepository) ListByBinding(ctx context.Context, bindingID string) ([]OutputManifestKey, error) {
	var rows []domain.OutputManifest
	if err := r.db.WithContext(ctx).Where("c_binding_id = ?", strings.TrimSpace(bindingID)).Order("c_subject_id, c_frequency, c_period_time").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]OutputManifestKey, 0, len(rows))
	for _, row := range rows {
		result = append(result, OutputManifestKey{BindingID: row.BindingID, SubjectID: row.SubjectID, Frequency: row.Frequency, PeriodTime: time.Unix(0, row.PeriodTime).UTC()})
	}
	return result, nil
}

func (r *OutputManifestRepository) Replace(ctx context.Context, key OutputManifestKey, keys []string) error {
	normalized := normalizeManifestKeys(keys)
	if len(normalized) == 0 {
		return r.db.WithContext(ctx).Where(
			"c_binding_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_period_time = ?",
			strings.TrimSpace(key.BindingID), strings.TrimSpace(key.SubjectID), strings.TrimSpace(key.Frequency), key.PeriodTime.UTC().UnixNano(),
		).Delete(&domain.OutputManifest{}).Error
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	row := domain.OutputManifest{
		BindingID: strings.TrimSpace(key.BindingID), SubjectID: strings.TrimSpace(key.SubjectID),
		Frequency: strings.TrimSpace(key.Frequency), PeriodTime: key.PeriodTime.UTC().UnixNano(),
		RowKeysJSON: string(raw), UpdatedAt: time.Now().UTC(),
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_binding_id"}, {Name: "c_subject_id"}, {Name: "c_frequency"}, {Name: "c_period_time"}},
		DoUpdates: clause.AssignmentColumns([]string{"c_row_keys_json", "c_updated_at"}),
	}).Create(&row).Error
}

// DeleteBefore bounds the manifest table without touching current output
// rows. Result View retention is the source of truth for old values; the
// manifest is only needed to clear recent rows during a correction or
// lifecycle mutation.
func (r *OutputManifestRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("output manifest repository is not initialized")
	}
	result := r.db.WithContext(ctx).Where("c_period_time < ?", before.UTC().UnixNano()).Delete(&domain.OutputManifest{})
	return result.RowsAffected, result.Error
}

func normalizeManifestKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	slices.Sort(normalized)
	return normalized
}
