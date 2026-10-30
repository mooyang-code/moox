package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error) {
	if item == nil {
		return nil, errors.New("dataset is required")
	}
	if _, err := s.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err == nil {
		return s.UpdateDataset(ctx, item)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.CreateDataset(ctx, item)
}

func (s *Store) CreateDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error) {
	item, err := normalizeDatasetForCreate(item)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireActiveDataNode(ctx, tx, item.GetDataNodeId()); err != nil {
		return nil, err
	}
	item.SubjectTags = normalizeSubjectTags(item.GetSubjectTags())
	if err := validateSubjectTagsExist(ctx, tx, item.GetSpaceId(), item.GetSubjectTags()); err != nil {
		return nil, err
	}
	item.Status = "disabled"
	item.BindingLocked = false
	item.Revision = 1
	now := s.nowUTC().Format(time.RFC3339Nano)
	item.CreatedAt = now
	item.UpdatedAt = now
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	subjectTags, err := marshalJSON(item.GetSubjectTags())
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_datasets (
			c_space_id, c_dataset_id, c_data_source_id, c_data_node_id, c_name,
			c_description, c_data_kind, c_freq,
			c_binding_locked, c_revision, c_status, c_attrs_json, c_subject_tags_json, c_ctime, c_mtime
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetDatasetId(), nullableDatasetSource(item.GetDataSourceId()), item.GetDataNodeId(), item.GetName(), item.GetDescription(), dataKindSQL(item.GetDataKind()), item.GetFreq(), boolInt(item.GetBindingLocked()), item.GetRevision(), item.GetStatus(), raw, subjectTags, now, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) UpdateDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error) {
	if item == nil || strings.TrimSpace(item.GetSpaceId()) == "" || strings.TrimSpace(item.GetDatasetId()) == "" || strings.TrimSpace(item.GetName()) == "" {
		return nil, errors.New("space_id, dataset_id and name are required")
	}
	item = proto.Clone(item).(*pb.Dataset)
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.DatasetId = strings.TrimSpace(item.GetDatasetId())
	item.Name = strings.TrimSpace(item.GetName())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getDatasetTx(ctx, tx, item.GetSpaceId(), item.GetDatasetId())
	if err != nil {
		return nil, err
	}
	if item.GetRevision() != 0 && item.GetRevision() != existing.GetRevision() {
		return nil, ErrRevisionConflict
	}
	if item.GetDataNodeId() != "" && strings.TrimSpace(item.GetDataNodeId()) != existing.GetDataNodeId() {
		return nil, errors.New("dataset data_node_id is immutable; use rebind")
	}
	// data_source_id is immutable once set; a Dataset created without one may
	// have it assigned later.
	dataSourceID := existing.GetDataSourceId()
	if candidate := strings.TrimSpace(item.GetDataSourceId()); candidate != "" && candidate != dataSourceID {
		if dataSourceID != "" {
			return nil, errors.New("dataset data_source_id is immutable")
		}
		dataSourceID = candidate
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_UNSPECIFIED && item.GetDataKind() != existing.GetDataKind() {
		return nil, errors.New("dataset data_kind is immutable")
	}
	if freq := strings.TrimSpace(item.GetFreq()); freq != "" && freq != existing.GetFreq() {
		return nil, errors.New("dataset freq is immutable")
	}
	status := existing.GetStatus()
	if candidate := strings.TrimSpace(item.GetStatus()); candidate != "" {
		if err := validateDatasetStatus(candidate); err != nil {
			return nil, err
		}
		if existing.GetStatus() == "disabled" && candidate == "active" {
			return nil, ErrDatasetMustBeDisabled
		}
		status = candidate
	}
	item.DataSourceId = dataSourceID
	item.DataNodeId = existing.GetDataNodeId()
	item.DataKind = existing.GetDataKind()
	item.Freq = existing.GetFreq()
	item.Retention, item.RetentionSource = "", ""
	item.SubjectTags = normalizeSubjectTags(item.GetSubjectTags())
	if err := validateSubjectTagsExist(ctx, tx, item.GetSpaceId(), item.GetSubjectTags()); err != nil {
		return nil, err
	}
	item.Status = status
	item.BindingLocked = existing.GetBindingLocked()
	item.Revision = existing.GetRevision() + 1
	item.CreatedAt = existing.GetCreatedAt()
	item.UpdatedAt = s.nowUTC().Format(time.RFC3339Nano)
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	subjectTags, err := marshalJSON(item.GetSubjectTags())
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE t_datasets SET
			c_data_source_id = ?, c_name = ?, c_description = ?,
			c_status = ?, c_attrs_json = ?, c_subject_tags_json = ?, c_revision = c_revision + 1, c_mtime = ?
		WHERE c_space_id = ? AND c_dataset_id = ? AND c_revision = ?
	`, nullableDatasetSource(dataSourceID), item.GetName(), item.GetDescription(), item.GetStatus(), raw, subjectTags, item.GetUpdatedAt(), item.GetSpaceId(), item.GetDatasetId(), existing.GetRevision())
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	return scanMessageWithSQLTimestamps(s.queryDB(ctx).QueryRowContext(ctx, `
		SELECT c_attrs_json, c_ctime, c_mtime FROM t_datasets
		WHERE c_space_id = ? AND c_dataset_id = ?
	`, spaceID, datasetID), func() *pb.Dataset { return &pb.Dataset{} })
}

func (s *Store) DeleteDataset(ctx context.Context, spaceID string, datasetID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := getDatasetTx(ctx, tx, spaceID, datasetID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Store) GetDatasetColumn(ctx context.Context, spaceID string, datasetID string, columnName string) (*pb.DatasetColumn, error) {
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_dataset_columns WHERE c_space_id = ? AND c_dataset_id = ? AND c_column_name = ?`, []any{spaceID, datasetID, columnName}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
}

type DatasetQuery = coremetadata.DatasetQuery

func (s *Store) ListDatasets(ctx context.Context, query coremetadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	where := []string{"1 = 1"}
	args := make([]any, 0, 12+len(query.DataNodeIDs))
	if spaceID := strings.TrimSpace(query.SpaceID); spaceID != "" {
		where = append(where, "c_space_id = ?")
		args = append(args, spaceID)
	}
	if dataSourceID := strings.TrimSpace(query.DataSourceID); dataSourceID != "" {
		where = append(where, "c_data_source_id = ?")
		args = append(args, dataSourceID)
	}
	if dataNodeID := strings.TrimSpace(query.DataNodeID); dataNodeID != "" {
		where = append(where, "c_data_node_id = ?")
		args = append(args, dataNodeID)
	}
	dataNodeIDs := make([]string, 0, len(query.DataNodeIDs))
	for _, nodeID := range query.DataNodeIDs {
		if nodeID = strings.TrimSpace(nodeID); nodeID != "" {
			dataNodeIDs = append(dataNodeIDs, nodeID)
		}
	}
	if len(dataNodeIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(dataNodeIDs)), ",")
		where = append(where, "c_data_node_id IN ("+placeholders+")")
		for _, nodeID := range dataNodeIDs {
			args = append(args, nodeID)
		}
	}
	if kind := dataKindFilter(query.DataKind); kind != "" {
		where = append(where, "c_data_kind = ?")
		args = append(args, kind)
	}
	if freq := strings.TrimSpace(query.Freq); freq != "" {
		where = append(where, "c_freq = ?")
		args = append(args, freq)
	}
	whereSQL := strings.Join(where, " AND ")
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json FROM t_datasets WHERE `+whereSQL+` ORDER BY c_space_id, c_dataset_id`,
		`SELECT COUNT(1) FROM t_datasets WHERE `+whereSQL,
		args, query.Page, func() *pb.Dataset { return &pb.Dataset{} },
	)
}

func (s *Store) RebindDatasetDataNode(ctx context.Context, spaceID, datasetID, nodeID string, expectedRevision uint64) (*pb.Dataset, error) {
	spaceID = strings.TrimSpace(spaceID)
	datasetID = strings.TrimSpace(datasetID)
	nodeID = strings.TrimSpace(nodeID)
	if spaceID == "" || datasetID == "" || nodeID == "" {
		return nil, errors.New("space_id, dataset_id and node_id are required")
	}
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getDatasetTx(ctx, tx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	if existing.GetRevision() != expectedRevision {
		return nil, ErrRevisionConflict
	}
	if existing.GetStatus() != "disabled" {
		return nil, ErrDatasetMustBeDisabled
	}
	if existing.GetBindingLocked() {
		return nil, ErrBindingLocked
	}
	if existing.GetDataNodeId() == nodeID {
		return nil, errors.New("dataset is already bound to this data node")
	}
	if err := requireActiveDataNode(ctx, tx, nodeID); err != nil {
		return nil, err
	}
	updated := proto.Clone(existing).(*pb.Dataset)
	updated.DataNodeId = nodeID
	updated.Revision++
	updated.UpdatedAt = s.nowUTC().Format(time.RFC3339Nano)
	raw, err := marshal(updated)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE t_datasets
		SET c_data_node_id = ?, c_revision = c_revision + 1, c_attrs_json = ?, c_mtime = ?
		WHERE c_space_id = ? AND c_dataset_id = ? AND c_revision = ?
	`, nodeID, raw, updated.GetUpdatedAt(), spaceID, datasetID, expectedRevision)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) CommitDatasetActivation(ctx context.Context, spaceID, datasetID string, expectedRevision uint64) (*pb.Dataset, error) {
	spaceID = strings.TrimSpace(spaceID)
	datasetID = strings.TrimSpace(datasetID)
	if spaceID == "" || datasetID == "" {
		return nil, errors.New("space_id and dataset_id are required")
	}
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getDatasetTx(ctx, tx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	if existing.GetStatus() == "active" && existing.GetBindingLocked() {
		return existing, nil
	}
	if existing.GetStatus() == "active" {
		return nil, ErrDatasetMustBeDisabled
	}
	if existing.GetRevision() != expectedRevision {
		return nil, ErrRevisionConflict
	}
	if err := requireActiveDataNode(ctx, tx, existing.GetDataNodeId()); err != nil {
		return nil, err
	}
	updated := proto.Clone(existing).(*pb.Dataset)
	updated.Status = "active"
	updated.BindingLocked = true
	updated.Revision++
	updated.UpdatedAt = s.nowUTC().Format(time.RFC3339Nano)
	raw, err := marshal(updated)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE t_datasets
		SET c_status = 'active', c_binding_locked = 1, c_revision = c_revision + 1,
			c_attrs_json = ?, c_mtime = ?
		WHERE c_space_id = ? AND c_dataset_id = ? AND c_revision = ?
	`, raw, updated.GetUpdatedAt(), spaceID, datasetID, expectedRevision)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func normalizeDatasetForCreate(item *pb.Dataset) (*pb.Dataset, error) {
	if item == nil {
		return nil, errors.New("dataset is required")
	}
	item = proto.Clone(item).(*pb.Dataset)
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.DatasetId = strings.TrimSpace(item.GetDatasetId())
	item.DataSourceId = strings.TrimSpace(item.GetDataSourceId())
	item.DataNodeId = strings.TrimSpace(item.GetDataNodeId())
	item.Name = strings.TrimSpace(item.GetName())
	if item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDataNodeId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, dataset_id, data_node_id and name are required")
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_RECORD && item.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return nil, errors.New("data_kind must be record or time_series")
	}
	// Retention is resolved from the Storage policy on read, never stored.
	item.Retention, item.RetentionSource = "", ""
	item.Freq = strings.TrimSpace(item.GetFreq())
	if item.GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES && !frequencypkg.IsCanonical(item.GetFreq()) {
		return nil, fmt.Errorf("time_series dataset freq must be one of %s: %q", strings.Join(frequencypkg.StandardStrings(), ", "), item.GetFreq())
	}
	return item, nil
}

func nullableDatasetSource(sourceID string) any {
	if sourceID = strings.TrimSpace(sourceID); sourceID != "" {
		return sourceID
	}
	return nil
}

func validateDatasetStatus(status string) error {
	if status != "active" && status != "disabled" {
		return fmt.Errorf("dataset status must be active or disabled: %q", status)
	}
	return nil
}

func getDatasetTx(ctx context.Context, tx queryRower, spaceID, datasetID string) (*pb.Dataset, error) {
	return scanMessageWithSQLTimestamps(tx.QueryRowContext(ctx, `
		SELECT c_attrs_json, c_ctime, c_mtime FROM t_datasets
		WHERE c_space_id = ? AND c_dataset_id = ?
	`, spaceID, datasetID), func() *pb.Dataset { return &pb.Dataset{} })
}

func requireActiveDataNode(ctx context.Context, tx queryRower, nodeID string) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT c_status FROM t_data_nodes WHERE c_node_id = ?`, nodeID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("data node %q: %w", nodeID, sql.ErrNoRows)
		}
		return err
	}
	if status != "active" {
		return ErrDataNodeDisabled
	}
	return nil
}

func (s *Store) UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error) {
	raw, err := s.prepareField(ctx, item)
	if err != nil {
		return nil, err
	}
	if existing, getErr := s.GetField(ctx, item.GetSpaceId(), item.GetFieldId()); getErr == nil {
		if existing.GetValueType() != item.GetValueType() {
			return nil, errors.New("field value_type is immutable")
		}
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, getErr
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_fields (c_space_id, c_field_id, c_group_id, c_name, c_description, c_value_type, c_unit, c_validation_rule_json, c_write_example, c_sort_order, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_field_id) DO UPDATE SET
			c_group_id = excluded.c_group_id,
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_value_type = excluded.c_value_type,
			c_unit = excluded.c_unit,
			c_validation_rule_json = excluded.c_validation_rule_json,
			c_write_example = excluded.c_write_example,
			c_sort_order = excluded.c_sort_order,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetFieldId(), item.GetGroupId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetUnit(), defaultJSON(item.GetValidationRuleJson()), item.GetWriteExample(), item.GetSortOrder(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetField(ctx, item.GetSpaceId(), item.GetFieldId())
}

func (s *Store) CreateField(ctx context.Context, item *pb.Field) (*pb.Field, error) {
	raw, err := s.prepareField(ctx, item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_fields (c_space_id, c_field_id, c_group_id, c_name, c_description, c_value_type, c_unit, c_validation_rule_json, c_write_example, c_sort_order, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetFieldId(), item.GetGroupId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetUnit(), defaultJSON(item.GetValidationRuleJson()), item.GetWriteExample(), item.GetSortOrder(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetField(ctx, item.GetSpaceId(), item.GetFieldId())
}

func (s *Store) UpdateField(ctx context.Context, item *pb.Field) (*pb.Field, error) {
	raw, err := s.prepareField(ctx, item)
	if err != nil {
		return nil, err
	}
	if existing, getErr := s.GetField(ctx, item.GetSpaceId(), item.GetFieldId()); getErr != nil {
		return nil, getErr
	} else if existing.GetValueType() != item.GetValueType() {
		return nil, errors.New("field value_type is immutable")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE t_fields SET c_group_id = ?, c_name = ?, c_description = ?, c_value_type = ?, c_unit = ?,
			c_validation_rule_json = ?, c_write_example = ?, c_sort_order = ?, c_status = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_field_id = ?
	`, item.GetGroupId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetUnit(), defaultJSON(item.GetValidationRuleJson()), item.GetWriteExample(), item.GetSortOrder(), item.GetStatus(), raw, item.GetSpaceId(), item.GetFieldId())
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetField(ctx, item.GetSpaceId(), item.GetFieldId())
}

func (s *Store) prepareField(ctx context.Context, item *pb.Field) (string, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetFieldId() == "" || item.GetName() == "" || item.GetGroupId() == "" {
		return "", errors.New("space_id, field_id, name and group_id are required")
	}
	if _, err := s.GetFieldGroup(ctx, item.GetSpaceId(), item.GetGroupId()); err != nil {
		return "", err
	}
	item.Status = defaultStatus(item.GetStatus())
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return marshal(item)
}

func (s *Store) GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error) {
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_fields WHERE c_space_id = ? AND c_field_id = ?`, []any{spaceID, fieldID}, func() *pb.Field { return &pb.Field{} })
}

func (s *Store) ListFields(ctx context.Context, query coremetadata.FieldQuery) ([]*pb.Field, *pb.PageResult, error) {
	if query.GroupID != "" && query.UngroupedOnly {
		return nil, nil, errors.New("group_id and ungrouped_only cannot be used together")
	}
	sortColumns := map[string]string{
		"":           "c_sort_order",
		"sort_order": "c_sort_order",
		"field_id":   "c_field_id",
		"updated_at": "c_mtime",
	}
	sortColumn, ok := sortColumns[query.SortBy]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported field sort %q", query.SortBy)
	}
	direction := strings.ToUpper(query.SortOrder)
	if direction == "" {
		direction = "ASC"
	}
	if direction != "ASC" && direction != "DESC" {
		return nil, nil, fmt.Errorf("unsupported field sort order %q", query.SortOrder)
	}

	where := []string{"1 = 1"}
	args := make([]any, 0, 12)
	if query.SpaceID != "" {
		where = append(where, "c_space_id = ?")
		args = append(args, query.SpaceID)
	}
	if query.UngroupedOnly {
		where = append(where, "COALESCE(c_group_id, '') = ''")
	} else if query.GroupID != "" && query.IncludeDescendants {
		where = append(where, "c_group_id IN (SELECT c_group_id FROM t_field_groups WHERE c_space_id = ? AND (c_group_id = ? OR c_parent_group_id = ?))")
		args = append(args, query.SpaceID, query.GroupID, query.GroupID)
	} else if query.GroupID != "" {
		where = append(where, "c_group_id = ?")
		args = append(args, query.GroupID)
	}
	if valueType := valueTypeFilter(query.ValueType); valueType != "" {
		where = append(where, "c_value_type = ?")
		args = append(args, valueType)
	}
	if query.Status != "" {
		where = append(where, "c_status = ?")
		args = append(args, query.Status)
	}
	if keyword := strings.ToLower(strings.TrimSpace(query.Keyword)); keyword != "" {
		pattern := "%" + escapeLikePattern(keyword) + "%"
		where = append(where, `(LOWER(c_field_id) LIKE ? ESCAPE '\' OR LOWER(c_name) LIKE ? ESCAPE '\' OR LOWER(c_description) LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern)
	}

	whereSQL := strings.Join(where, " AND ")
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json FROM t_fields WHERE `+whereSQL+` ORDER BY `+sortColumn+` `+direction+`, c_field_id `+direction,
		`SELECT COUNT(1) FROM t_fields WHERE `+whereSQL,
		args, query.Page, func() *pb.Field { return &pb.Field{} },
	)
}

func escapeLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (s *Store) UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, dataset_id and column_name are required")
	}
	if item.GetValueType() == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
		return nil, errors.New("dataset column value_type must be declared")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	aliases, err := marshalJSON(item.GetAliases())
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, getErr := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_dataset_columns WHERE c_space_id = ? AND c_dataset_id = ? AND c_column_name = ?`,
		[]any{item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName()}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	newColumn := errors.Is(getErr, sql.ErrNoRows)
	if getErr == nil {
		if existing.GetOriginType() != item.GetOriginType() || existing.GetOriginId() != item.GetOriginId() || existing.GetValueType() != item.GetValueType() {
			return nil, errors.New("dataset column identity and value_type are immutable")
		}
	} else if !newColumn {
		return nil, getErr
	}
	wasActive := getErr == nil && (existing.GetStatus() == "" || existing.GetStatus() == "active")
	isActive := item.GetStatus() == "active"
	projectionChanged := wasActive != isActive
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_aliases_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_value_type = excluded.c_value_type,
			c_aliases_json = excluded.c_aliases_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName(), datasetOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), aliases, item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	if (newColumn || projectionChanged) && isActive {
		if err := addColumnToFollowingViews(ctx, tx, item); err != nil {
			return nil, err
		}
	} else if isActive && wasActive && getErr == nil && !maps.Equal(existing.GetAttributes(), item.GetAttributes()) {
		// 已有列只改了属性：跟随它的 View 的列要带上新属性。
		if err := refreshFollowingViewColumnAttributes(ctx, tx, item); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// followingViewIDs 返回数据集上声明了 follow_dataset_columns=true 的活动 View：这些 View 跟随数据集加列。
func followingViewIDs(ctx context.Context, tx *sql.Tx, spaceID, datasetID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c_view_id FROM t_views
		WHERE c_space_id = ? AND c_dataset_id = ? AND c_status = 'active'
		  AND json_extract(c_attrs_json, '$.attributes.follow_dataset_columns') = 'true'
		ORDER BY c_view_id`, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// addColumnToFollowingViews 把数据集的新列（或重新启用的列）加进跟随它的 View，由定时任务原地追加到索引。
func addColumnToFollowingViews(ctx context.Context, tx *sql.Tx, item *pb.DatasetColumn) error {
	viewIDs, err := followingViewIDs(ctx, tx, item.GetSpaceId(), item.GetDatasetId())
	if err != nil {
		return err
	}
	for _, viewID := range viewIDs {
		if err := upsertViewColumn(ctx, tx, &pb.ViewColumn{
			SpaceId: item.GetSpaceId(), ViewId: viewID, ColumnName: item.GetColumnName(),
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: item.GetColumnName(),
			ValueType: item.GetValueType(), Attributes: cloneMetadataStringMap(item.GetAttributes()),
		}); err != nil {
			return err
		}
	}
	return nil
}

// refreshFollowingViewColumnAttributes 把数据集列的新属性同步到跟随它的 View 的同名列：只改属性，排序与其他字段不动；
// View 里还没有这一列（没有加进 View）时什么都不做。
func refreshFollowingViewColumnAttributes(ctx context.Context, tx *sql.Tx, item *pb.DatasetColumn) error {
	viewIDs, err := followingViewIDs(ctx, tx, item.GetSpaceId(), item.GetDatasetId())
	if err != nil {
		return err
	}
	for _, viewID := range viewIDs {
		column, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ? AND c_column_name = ?`,
			[]any{item.GetSpaceId(), viewID, item.GetColumnName()}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		column.Attributes = cloneMetadataStringMap(item.GetAttributes())
		raw, err := marshal(column)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE t_view_columns SET c_attrs_json = ? WHERE c_space_id = ? AND c_view_id = ? AND c_column_name = ?`,
			raw, item.GetSpaceId(), viewID, item.GetColumnName()); err != nil {
			return err
		}
	}
	return nil
}

func cloneMetadataStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (s *Store) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	const where = `
		FROM t_dataset_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_dataset_id, c_column_name`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.DatasetColumn { return &pb.DatasetColumn{} },
	)
}

func (s *Store) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	return queryMessages(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_views WHERE (? = '' OR c_space_id = ?) AND (? = '' OR c_dataset_id = ?) AND c_status = 'active' ORDER BY c_space_id, c_view_id`, []any{spaceID, spaceID, datasetID, datasetID}, func() *pb.View { return &pb.View{} })
}
