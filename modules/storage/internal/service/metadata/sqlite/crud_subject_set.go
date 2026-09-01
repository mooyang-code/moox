package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	shardPrefix := setID + "::shard:"
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c_set_id, c_dataset_id, c_status FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND (c_set_id = ? OR c_set_id LIKE ?)`, spaceID, setID, shardPrefix+"%")
	if err != nil {
		return 0, err
	}
	var datasetIDs []string
	status := ""
	shardCount := 0
	shardIndexes := make(map[int]struct{})
	sharded := false
	for rows.Next() {
		var datasetID string
		var stagedSetID string
		var rowStatus string
		if err := rows.Scan(&stagedSetID, &datasetID, &rowStatus); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if stagedSetID != setID {
			sharded = true
			base, index, count, ok := parseDatasetSubjectShardSetID(stagedSetID)
			if !ok || base != setID || index < 0 || count <= 0 || index >= count {
				_ = rows.Close()
				return 0, fmt.Errorf("invalid staged dataset subject shard id %q", stagedSetID)
			}
			if shardCount == 0 {
				shardCount = count
			} else if shardCount != count {
				_ = rows.Close()
				return 0, fmt.Errorf("staged dataset subject shards %s have mixed counts", setID)
			}
			shardIndexes[index] = struct{}{}
		}
		if status == "" {
			status = rowStatus
		} else if status != rowStatus && !sharded {
			_ = rows.Close()
			return 0, fmt.Errorf("staged dataset subject set %s/%s has mixed statuses", spaceID, setID)
		}
		if !containsString(datasetIDs, datasetID) {
			datasetIDs = append(datasetIDs, datasetID)
		}
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
	if sharded {
		if len(shardIndexes) != shardCount {
			// A shard is allowed to arrive before its peers. Returning a zero
			// count keeps this normal asynchronous state out of the failure
			// path and leaves the old active set untouched.
			return 0, nil
		}
		for index := 0; index < shardCount; index++ {
			if _, ok := shardIndexes[index]; !ok {
				return 0, nil
			}
		}
	}
	setWhere := `c_set_id = ?`
	setArgs := []any{setID}
	if sharded {
		setWhere = `(c_set_id = ? OR c_set_id LIKE ?)`
		setArgs = []any{setID, shardPrefix + "%"}
	}
	stagedFingerprint, err := validateInstrumentSnapshotGeneration(ctx, tx, `
		SELECT c_active_status, c_attrs_json FROM t_dataset_subject_set_staging
		WHERE c_space_id = ? AND `+setWhere, append([]any{spaceID}, setArgs...)...)
	if err != nil {
		return 0, err
	}
	if status == "activated" {
		var count int
		queryArgs := append([]any{spaceID}, setArgs...)
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND `+setWhere, queryArgs...).Scan(&count); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return count, nil
	}
	stagedArgs := append([]any{spaceID}, setArgs...)
	stagedFetchedAt, err := maxInstrumentFetchedAt(ctx, tx, `
		SELECT c_attrs_json FROM t_dataset_subject_set_staging
		WHERE c_space_id = ? AND `+setWhere, stagedArgs...)
	if err != nil {
		return 0, err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(datasetIDs)), ",")
	activeArgs := make([]any, 0, len(datasetIDs)+1)
	activeArgs = append(activeArgs, spaceID)
	for _, datasetID := range datasetIDs {
		activeArgs = append(activeArgs, datasetID)
	}
	activeFetchedAt, err := maxInstrumentFetchedAt(ctx, tx, fmt.Sprintf(`
		SELECT c_attrs_json FROM t_dataset_subjects WHERE c_space_id = ? AND c_dataset_id IN (%s)`, placeholders), activeArgs...)
	if err != nil {
		return 0, err
	}
	activeFingerprint, err := instrumentSnapshotFingerprint(ctx, tx, fmt.Sprintf(`
		SELECT c_attrs_json FROM t_dataset_subjects WHERE c_space_id = ? AND c_status = 'active' AND c_dataset_id IN (%s)`, placeholders), activeArgs...)
	if err != nil {
		return 0, err
	}
	if !stagedFetchedAt.IsZero() && stagedFetchedAt.Before(activeFetchedAt) {
		return 0, ErrRevisionConflict
	}
	if !stagedFetchedAt.IsZero() && stagedFetchedAt.Equal(activeFetchedAt) && stagedFingerprint != "" && activeFingerprint != "" && stagedFingerprint != activeFingerprint {
		return 0, fmt.Errorf("%w: staged instrument snapshot fingerprint differs from active generation", ErrRevisionConflict)
	}
	for _, datasetID := range datasetIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_dataset_subjects WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID); err != nil {
			return 0, err
		}
	}
	insertArgs := append([]any{spaceID}, setArgs...)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO t_dataset_subjects
			(c_space_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_status, c_attrs_json)
		SELECT c_space_id, c_dataset_id, c_subject_id, c_subject_role, c_effective_start_time, c_effective_end_time, c_active_status, c_attrs_json
		FROM t_dataset_subject_set_staging WHERE c_space_id = ? AND `+setWhere,
		insertArgs...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	updateArgs := append([]any{spaceID}, setArgs...)
	if _, err := tx.ExecContext(ctx, `UPDATE t_dataset_subject_set_staging SET c_status = 'activated' WHERE c_space_id = ? AND `+setWhere, updateArgs...); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func parseDatasetSubjectShardSetID(setID string) (string, int, int, bool) {
	const marker = "::shard:"
	position := strings.LastIndex(setID, marker)
	if position <= 0 {
		return "", 0, 0, false
	}
	parts := strings.Split(setID[position+len(marker):], "/")
	if len(parts) != 2 {
		return "", 0, 0, false
	}
	index, indexErr := strconv.Atoi(parts[0])
	count, countErr := strconv.Atoi(parts[1])
	if indexErr != nil || countErr != nil {
		return "", 0, 0, false
	}
	return setID[:position], index, count, true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// maxInstrumentFetchedAt is evaluated inside the activation transaction. This
// turns the pipeline's monotonic snapshot check into a storage-side CAS rather
// than a read-then-write race between two collector invocations.
func maxInstrumentFetchedAt(ctx context.Context, tx *sql.Tx, query string, args ...any) (time.Time, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var latest time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return time.Time{}, err
		}
		var binding pb.DatasetSubject
		if unmarshalOptions.Unmarshal([]byte(raw), &binding) != nil {
			continue
		}
		fetchedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(binding.GetAttributes()["active_instrument_set_fetched_at"]))
		if err != nil {
			continue
		}
		if latest.IsZero() || fetchedAt.After(latest) {
			latest = fetchedAt
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, err
	}
	return latest, nil
}

// validateInstrumentSnapshotGeneration prevents shards built from different
// provider snapshots from activating under one generation fence. Generic
// dataset subject sets do not carry the marker and retain their legacy path.
func validateInstrumentSnapshotGeneration(ctx context.Context, tx *sql.Tx, query string, args ...any) (string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var expected string
	sawFingerprint := false
	sawMissing := false
	for rows.Next() {
		var activeStatus, raw string
		if err := rows.Scan(&activeStatus, &raw); err != nil {
			return "", err
		}
		if activeStatus != "active" {
			continue
		}
		var binding pb.DatasetSubject
		if err := unmarshalOptions.Unmarshal([]byte(raw), &binding); err != nil {
			return "", err
		}
		fingerprint := strings.TrimSpace(binding.GetAttributes()["instrument_snapshot_fingerprint"])
		if fingerprint == "" {
			sawMissing = true
			continue
		}
		if expected == "" {
			expected = fingerprint
		} else if expected != fingerprint {
			return "", fmt.Errorf("%w: staged instrument snapshot fingerprints differ", ErrRevisionConflict)
		}
		sawFingerprint = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if sawFingerprint && sawMissing {
		return "", fmt.Errorf("%w: staged instrument snapshot fingerprint is missing", ErrRevisionConflict)
	}
	return expected, nil
}

// instrumentSnapshotFingerprint reads the active generation's content fence
// inside the same activation transaction. Legacy rows without this marker are
// tolerated so the first managed snapshot can migrate them forward.
func instrumentSnapshotFingerprint(ctx context.Context, tx *sql.Tx, query string, args ...any) (string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var expected string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return "", err
		}
		var binding pb.DatasetSubject
		if err := unmarshalOptions.Unmarshal([]byte(raw), &binding); err != nil {
			return "", err
		}
		fingerprint := strings.TrimSpace(binding.GetAttributes()["instrument_snapshot_fingerprint"])
		if fingerprint == "" {
			continue
		}
		if expected == "" {
			expected = fingerprint
			continue
		}
		if expected != fingerprint {
			return "", fmt.Errorf("%w: active instrument snapshot fingerprints differ", ErrRevisionConflict)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return expected, nil
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
