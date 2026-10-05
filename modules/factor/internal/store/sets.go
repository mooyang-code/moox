package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

type factorSetRow struct {
	SetID           string    `gorm:"column:c_set_id"`
	SpaceID         string    `gorm:"column:c_space_id"`
	SourceDatasetID string    `gorm:"column:c_source_dataset_id"`
	Freq            string    `gorm:"column:c_freq"`
	SubjectMode     string    `gorm:"column:c_subject_mode"`
	SubjectsJSON    string    `gorm:"column:c_subjects_json"`
	ResultDatasetID string    `gorm:"column:c_result_dataset_id"`
	Status          string    `gorm:"column:c_status"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
}

func (r factorSetRow) domain() (domain.FactorSet, error) {
	subjects, err := unmarshalStringSlice(r.SubjectsJSON)
	if err != nil {
		return domain.FactorSet{}, fmt.Errorf("decode factor set subjects: %w", err)
	}
	return domain.FactorSet{
		SetID: r.SetID, SpaceID: r.SpaceID, SourceDatasetID: r.SourceDatasetID,
		Freq: r.Freq, SubjectMode: r.SubjectMode, Subjects: subjects,
		ResultDatasetID: r.ResultDatasetID, Status: r.Status,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateSet creates one factor set. A (space, source dataset, frequency)
// uniqueness violation is returned as ErrConflict.
func (s *Store) CreateSet(ctx context.Context, set domain.FactorSet) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	set.SetID = strings.TrimSpace(set.SetID)
	set.SpaceID = strings.TrimSpace(set.SpaceID)
	set.SourceDatasetID = strings.TrimSpace(set.SourceDatasetID)
	set.Freq = strings.TrimSpace(set.Freq)
	set.ResultDatasetID = strings.TrimSpace(set.ResultDatasetID)
	if set.SetID == "" || set.SpaceID == "" || set.SourceDatasetID == "" || set.Freq == "" || set.ResultDatasetID == "" {
		return fmt.Errorf("factor set identity is incomplete")
	}
	if set.SubjectMode == "" {
		set.SubjectMode = domain.SubjectModeAll
	}
	if set.Status == "" {
		set.Status = domain.SetStatusPending
	}
	if set.Subjects == nil {
		set.Subjects = []string{}
	}
	subjectsJSON, err := marshalStringSlice(set.Subjects)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if set.CreatedAt.IsZero() {
		set.CreatedAt = now
	}
	set.UpdatedAt = now
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_factor_sets
		(c_set_id, c_space_id, c_source_dataset_id, c_freq, c_subject_mode, c_subjects_json,
		 c_result_dataset_id, c_status, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, set.SetID, set.SpaceID, set.SourceDatasetID, set.Freq, set.SubjectMode, subjectsJSON,
		set.ResultDatasetID, set.Status, set.CreatedAt, set.UpdatedAt)
	if result.Error != nil {
		if isUniqueConstraint(result.Error) {
			return fmt.Errorf("%w: factor set already exists for source dataset and frequency", ErrConflict)
		}
		return result.Error
	}
	return nil
}

// UpdateSet changes only the set's subject scope. Set identity, result dataset,
// and lifecycle status are controlled by the lifecycle service.
func (s *Store) UpdateSet(ctx context.Context, set domain.FactorSet) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	set.SetID = strings.TrimSpace(set.SetID)
	if set.SetID == "" {
		return fmt.Errorf("factor set id is required")
	}
	if set.SubjectMode == "" {
		set.SubjectMode = domain.SubjectModeAll
	}
	if set.Subjects == nil {
		set.Subjects = []string{}
	}
	subjectsJSON, err := marshalStringSlice(set.Subjects)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_sets SET c_subject_mode = ?, c_subjects_json = ?, c_mtime = ? WHERE c_set_id = ?
	`, set.SubjectMode, subjectsJSON, time.Now().UTC(), set.SetID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SetSetStatus changes a set's lifecycle status only if it still matches
// expectedStatus. The single conditional update makes competing lifecycle
// transitions explicit instead of silently overwriting one another.
func (s *Store) SetSetStatus(ctx context.Context, setID, expectedStatus, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	setID = strings.TrimSpace(setID)
	if setID == "" {
		return fmt.Errorf("set id is required")
	}
	if !validSetStatus(expectedStatus) || !validSetStatus(status) {
		return fmt.Errorf("invalid factor set status transition %q to %q", expectedStatus, status)
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_sets SET c_status = ?, c_mtime = ? WHERE c_set_id = ? AND c_status = ?
	`, status, time.Now().UTC(), setID, expectedStatus)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	if _, err := s.GetSet(ctx, setID); err != nil {
		return err
	}
	return fmt.Errorf("%w: factor set %q status changed", ErrConflict, setID)
}

// GetSet returns a factor set by id.
func (s *Store) GetSet(ctx context.Context, setID string) (domain.FactorSet, error) {
	if s == nil || s.db == nil {
		return domain.FactorSet{}, fmt.Errorf("factor database is not open")
	}
	var row factorSetRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT c_set_id, c_space_id, c_source_dataset_id, c_freq, c_subject_mode,
		       c_subjects_json, c_result_dataset_id, c_status, c_ctime, c_mtime
		FROM t_factor_sets WHERE c_set_id = ?
	`, strings.TrimSpace(setID)).Scan(&row)
	if result.Error != nil {
		return domain.FactorSet{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorSet{}, gorm.ErrRecordNotFound
	}
	return row.domain()
}

// ListSets returns all factor sets in stable id order.
func (s *Store) ListSets(ctx context.Context) ([]domain.FactorSet, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []factorSetRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_set_id, c_space_id, c_source_dataset_id, c_freq, c_subject_mode,
		       c_subjects_json, c_result_dataset_id, c_status, c_ctime, c_mtime
		FROM t_factor_sets ORDER BY c_set_id
	`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sets := make([]domain.FactorSet, 0, len(rows))
	for _, row := range rows {
		set, err := row.domain()
		if err != nil {
			return nil, err
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// DeleteSet removes a factor set without members. SQLite's foreign key rejects
// deletion while members still reference it, which is reported as ErrConflict.
func (s *Store) DeleteSet(ctx context.Context, setID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	setID = strings.TrimSpace(setID)
	result := s.db.WithContext(ctx).Exec("DELETE FROM t_factor_sets WHERE c_set_id = ?", setID)
	if result.Error != nil {
		if isForeignKeyConstraint(result.Error) {
			return fmt.Errorf("%w: factor set %q still has members", ErrConflict, setID)
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func marshalStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	return string(data), err
}

func unmarshalStringSlice(raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

func isForeignKeyConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

func validSetStatus(status string) bool {
	switch status {
	case domain.SetStatusPending, domain.SetStatusEnabled, domain.SetStatusDisabled, domain.SetStatusDeleting:
		return true
	default:
		return false
	}
}
