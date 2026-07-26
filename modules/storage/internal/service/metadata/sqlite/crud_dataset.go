package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	item, keepDuration, err := normalizeDatasetForCreate(item)
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
	item.KeepDuration = keepDuration
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
	freqs, err := marshalJSON(item.GetFreqs())
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_datasets (
			c_space_id, c_dataset_id, c_data_source_id, c_data_node_id, c_name,
			c_description, c_data_kind, c_freqs_json, c_keep_duration,
			c_binding_locked, c_revision, c_status, c_attrs_json, c_ctime, c_mtime
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetDataSourceId(), item.GetDataNodeId(), item.GetName(), item.GetDescription(), dataKindSQL(item.GetDataKind()), freqs, item.GetKeepDuration(), boolInt(item.GetBindingLocked()), item.GetRevision(), item.GetStatus(), raw, now, now)
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
	if item.GetDataSourceId() != "" && item.GetDataSourceId() != existing.GetDataSourceId() {
		return nil, errors.New("dataset data_source_id is immutable")
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_UNSPECIFIED && item.GetDataKind() != existing.GetDataKind() {
		return nil, errors.New("dataset data_kind is immutable")
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
	keepDuration := item.GetKeepDuration()
	if keepDuration == "" {
		keepDuration = existing.GetKeepDuration()
	}
	keepDuration, err = normalizeKeepDuration(keepDuration, existing.GetDataKind())
	if err != nil {
		return nil, err
	}
	if err := validateDatasetKeepDuration(ctx, tx, item.GetSpaceId(), item.GetDatasetId(), keepDuration); err != nil {
		return nil, err
	}
	item.DataSourceId = existing.GetDataSourceId()
	item.DataNodeId = existing.GetDataNodeId()
	item.DataKind = existing.GetDataKind()
	item.KeepDuration = keepDuration
	item.Status = status
	item.BindingLocked = existing.GetBindingLocked()
	item.Revision = existing.GetRevision() + 1
	item.CreatedAt = existing.GetCreatedAt()
	item.UpdatedAt = s.nowUTC().Format(time.RFC3339Nano)
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	freqs, err := marshalJSON(item.GetFreqs())
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE t_datasets SET
			c_name = ?, c_description = ?, c_freqs_json = ?, c_keep_duration = ?,
			c_status = ?, c_attrs_json = ?, c_revision = c_revision + 1, c_mtime = ?
		WHERE c_space_id = ? AND c_dataset_id = ? AND c_revision = ?
	`, item.GetName(), item.GetDescription(), freqs, item.GetKeepDuration(), item.GetStatus(), raw, item.GetUpdatedAt(), item.GetSpaceId(), item.GetDatasetId(), existing.GetRevision())
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

func normalizeKeepDuration(value string, kind pb.DataKind) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "0"
	}
	if value == "0" {
		return value, nil
	}
	if kind == pb.DataKind_DATA_KIND_RECORD {
		return "", errors.New("record dataset keep_duration must be 0")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return "", fmt.Errorf("keep_duration must be 0 or a positive duration: %q", value)
	}
	return duration.String(), nil
}

func (s *Store) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	return scanMessageWithSQLTimestamps(s.queryDB(ctx).QueryRowContext(ctx, `
		SELECT c_attrs_json, c_ctime, c_mtime FROM t_datasets
		WHERE c_space_id = ? AND c_dataset_id = ?
	`, spaceID, datasetID), func() *pb.Dataset { return &pb.Dataset{} })
}

func (s *Store) DeleteDataset(ctx context.Context, spaceID string, datasetID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID)
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
	return nil
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
		where = append(where, "EXISTS (SELECT 1 FROM json_each(c_freqs_json) WHERE value = ?)")
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

func normalizeDatasetForCreate(item *pb.Dataset) (*pb.Dataset, string, error) {
	if item == nil {
		return nil, "", errors.New("dataset is required")
	}
	item = proto.Clone(item).(*pb.Dataset)
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.DatasetId = strings.TrimSpace(item.GetDatasetId())
	item.DataSourceId = strings.TrimSpace(item.GetDataSourceId())
	item.DataNodeId = strings.TrimSpace(item.GetDataNodeId())
	item.Name = strings.TrimSpace(item.GetName())
	if item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDataSourceId() == "" || item.GetDataNodeId() == "" || item.GetName() == "" {
		return nil, "", errors.New("space_id, dataset_id, data_source_id, data_node_id and name are required")
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_RECORD && item.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return nil, "", errors.New("data_kind must be record or time_series")
	}
	keepDuration, err := normalizeKeepDuration(item.GetKeepDuration(), item.GetDataKind())
	return item, keepDuration, err
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
	newField := false
	if existing, getErr := s.GetField(ctx, item.GetSpaceId(), item.GetFieldId()); getErr == nil {
		if existing.GetValueType() != item.GetValueType() {
			return nil, errors.New("field value_type is immutable")
		}
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, getErr
	} else {
		newField = true
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
	if newField {
		if err := s.bumpViewsForField(ctx, item.GetSpaceId(), item.GetFieldId()); err != nil {
			return nil, err
		}
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

func (s *Store) UpsertFactor(ctx context.Context, item *pb.Factor) (*pb.Factor, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetFactorId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, factor_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_factors (c_space_id, c_factor_id, c_name, c_description, c_algorithm, c_params_json, c_value_type, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_factor_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_algorithm = excluded.c_algorithm,
			c_params_json = excluded.c_params_json,
			c_value_type = excluded.c_value_type,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetFactorId(), item.GetName(), item.GetDescription(), item.GetAlgorithm(), defaultJSON(item.GetParamsJson()), valueTypeSQL(item.GetValueType()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetFactor(ctx, item.GetSpaceId(), item.GetFactorId())
}

func (s *Store) GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error) {
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_factors WHERE c_space_id = ? AND c_factor_id = ?`, []any{spaceID, factorID}, func() *pb.Factor { return &pb.Factor{} })
}

func (s *Store) ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	const where = `
		FROM t_factors
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_algorithm = ?)`
	args := []any{spaceID, spaceID, algorithm, algorithm}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_factor_id`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.Factor { return &pb.Factor{} },
	)
}

func (s *Store) UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, dataset_id and column_name are required")
	}
	if item.GetValueType() == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
		return nil, errors.New("dataset column value_type must be declared")
	}
	item.Status = defaultStatus(item.GetStatus())
	newColumn := false
	if existing, getErr := s.GetDatasetColumn(ctx, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName()); getErr == nil {
		if existing.GetOriginType() != item.GetOriginType() || existing.GetOriginId() != item.GetOriginId() || existing.GetValueType() != item.GetValueType() {
			return nil, errors.New("dataset column identity and value_type are immutable")
		}
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, getErr
	} else {
		newColumn = true
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	aliases, err := marshalJSON(item.GetAliases())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
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
	if newColumn {
		if err := s.bumpViewsForDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err != nil {
			return nil, err
		}
	}
	return item, nil
}

func (s *Store) bumpViewsForDataset(ctx context.Context, spaceID, datasetID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE t_views
		SET c_desired_view_revision = CASE WHEN c_desired_view_revision = 0 THEN 1 ELSE c_desired_view_revision + 1 END
		WHERE c_space_id = ? AND (
			c_primary_dataset_id = ? OR EXISTS (
				SELECT 1 FROM json_each(t_views.c_dataset_ids_json) ref WHERE ref.value = ?
			)
		)`, spaceID, datasetID, datasetID)
	return err
}

func (s *Store) bumpViewsForField(ctx context.Context, spaceID, fieldID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE t_views
		SET c_desired_view_revision = CASE WHEN c_desired_view_revision = 0 THEN 1 ELSE c_desired_view_revision + 1 END
		WHERE c_space_id = ? AND EXISTS (
			SELECT 1
			FROM t_dataset_columns column_ref
			WHERE column_ref.c_space_id = t_views.c_space_id
			  AND column_ref.c_origin_type = 'field'
			  AND column_ref.c_origin_id = ?
			  AND (
				column_ref.c_dataset_id = t_views.c_primary_dataset_id OR EXISTS (
					SELECT 1 FROM json_each(t_views.c_dataset_ids_json) ref WHERE ref.value = column_ref.c_dataset_id
				)
			  )
		)`, spaceID, fieldID)
	return err
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
	return queryMessages(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_views WHERE (? = '' OR c_space_id = ?) AND (? = '' OR c_primary_dataset_id = ? OR EXISTS (SELECT 1 FROM json_each(c_dataset_ids_json) ref WHERE ref.value = ?)) AND c_status = 'active' ORDER BY c_space_id, c_view_id`, []any{spaceID, spaceID, datasetID, datasetID, datasetID}, func() *pb.View { return &pb.View{} })
}
