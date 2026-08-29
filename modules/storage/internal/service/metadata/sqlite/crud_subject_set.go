package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// StageDatasetSubjectSet replaces one recoverable, inactive snapshot. It is a
// single transaction so a large snapshot is either fully staged or absent.
func (s *Store) StageDatasetSubjectSet(ctx context.Context, spaceID, setID string, bindings []*pb.DatasetSubject) (int, error) {
	spaceID = strings.TrimSpace(spaceID)
	setID = strings.TrimSpace(setID)
	if spaceID == "" || setID == "" || len(bindings) == 0 {
		return 0, errors.New("space_id, set_id and dataset_subjects are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND c_set_id = ?`, spaceID, setID); err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding == nil || binding.GetSpaceId() != spaceID || binding.GetDatasetId() == "" || binding.GetSubjectId() == "" {
			return 0, errors.New("staged dataset binding must match space_id and include dataset_id and subject_id")
		}
		key := binding.GetDatasetId() + "\x00" + binding.GetSubjectId()
		if _, ok := seen[key]; ok {
			return 0, fmt.Errorf("duplicate staged dataset binding %s/%s", binding.GetDatasetId(), binding.GetSubjectId())
		}
		seen[key] = struct{}{}
		status := defaultStatus(binding.GetStatus())
		if !validDatasetSubjectStatus(status) {
			return 0, fmt.Errorf("invalid staged active status %q", status)
		}
		role := binding.GetSubjectRole()
		if role == "" {
			role = "normal"
		}
		attrs := cloneStringMap(binding.GetAttributes())
		attrs["instrument_set_stage_id"] = setID
		raw, err := marshal(&pb.DatasetSubject{
			SpaceId: binding.GetSpaceId(), DatasetId: binding.GetDatasetId(), SubjectId: binding.GetSubjectId(),
			SubjectRole: role, EffectiveStartTime: binding.GetEffectiveStartTime(), EffectiveEndTime: binding.GetEffectiveEndTime(),
			Status: status, Attributes: attrs,
		})
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO t_dataset_subject_set_staging
				(c_space_id, c_set_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_status, c_active_status, c_attrs_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'building', ?, ?)
		`, spaceID, setID, binding.GetDatasetId(), binding.GetSubjectId(), role, binding.GetEffectiveStartTime(), binding.GetEffectiveEndTime(), status, raw); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(bindings), nil
}

// ActivateDatasetSubjectSet atomically swaps every dataset represented by a
// staged set. A crash before commit leaves the old active set and the staged
// rows available for retry; a crash during the transaction is rolled back by
// SQLite.
func (s *Store) ActivateDatasetSubjectSet(ctx context.Context, spaceID, setID string) (int, error) {
	spaceID = strings.TrimSpace(spaceID)
	setID = strings.TrimSpace(setID)
	if spaceID == "" || setID == "" {
		return 0, errors.New("space_id and set_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c_dataset_id FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND c_set_id = ?`, spaceID, setID)
	if err != nil {
		return 0, err
	}
	var datasetIDs []string
	for rows.Next() {
		var datasetID string
		if err := rows.Scan(&datasetID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		datasetIDs = append(datasetIDs, datasetID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(datasetIDs) == 0 {
		return 0, fmt.Errorf("staged dataset subject set %s/%s was not found", spaceID, setID)
	}
	for _, datasetID := range datasetIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_dataset_subjects WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID); err != nil {
			return 0, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO t_dataset_subjects
			(c_space_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_status, c_attrs_json)
		SELECT c_space_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_active_status, c_attrs_json
		FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND c_set_id = ?
	`, spaceID, setID)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND c_set_id = ?`, spaceID, setID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func validDatasetSubjectStatus(status string) bool {
	switch status {
	case "active", "disabled", "building", "archived", "deleted":
		return true
	default:
		return false
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input)+1)
	for key, value := range input {
		output[key] = value
	}
	return output
}

var _ interface {
	StageDatasetSubjectSet(context.Context, string, string, []*pb.DatasetSubject) (int, error)
	ActivateDatasetSubjectSet(context.Context, string, string) (int, error)
} = (*Store)(nil)
