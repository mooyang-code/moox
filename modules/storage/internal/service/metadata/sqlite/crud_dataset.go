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
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDataSourceId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, dataset_id, data_source_id and name are required")
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_RECORD && item.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return nil, errors.New("data_kind must be record or time_series")
	}
	keepDuration, err := normalizeKeepDuration(item.GetKeepDuration(), item.GetDataKind())
	if err != nil {
		return nil, err
	}
	item.KeepDuration = keepDuration
	if existing, getErr := s.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); getErr == nil {
		if existing.GetDataNodeId() != "" && item.GetDataNodeId() != "" && existing.GetDataNodeId() != item.GetDataNodeId() {
			return nil, errors.New("dataset data_node_id is immutable")
		}
		if item.GetDataNodeId() == "" {
			item.DataNodeId = existing.GetDataNodeId()
		}
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, getErr
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	freqs, err := marshalJSON(item.GetFreqs())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_datasets (c_space_id, c_dataset_id, c_data_source_id, c_data_node_id, c_name, c_description, c_data_kind, c_freqs_json, c_keep_duration, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id) DO UPDATE SET
			c_data_source_id = excluded.c_data_source_id,
			c_data_node_id = CASE WHEN excluded.c_data_node_id <> '' THEN excluded.c_data_node_id ELSE t_datasets.c_data_node_id END,
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_data_kind = excluded.c_data_kind,
			c_freqs_json = excluded.c_freqs_json,
			c_keep_duration = excluded.c_keep_duration,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetDataSourceId(), item.GetDataNodeId(), item.GetName(), item.GetDescription(), dataKindSQL(item.GetDataKind()), freqs, item.GetKeepDuration(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId())
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
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`, []any{spaceID, datasetID}, func() *pb.Dataset { return &pb.Dataset{} })
}

func (s *Store) GetDatasetColumn(ctx context.Context, spaceID string, datasetID string, columnName string) (*pb.DatasetColumn, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_dataset_columns WHERE c_space_id = ? AND c_dataset_id = ? AND c_column_name = ?`, []any{spaceID, datasetID, columnName}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
}

func (s *Store) ListDatasets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_datasets
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_data_source_id = ?)
		  AND (? = '' OR c_data_kind = ?)
		ORDER BY c_space_id, c_dataset_id
	`, []any{spaceID, spaceID, dataSourceID, dataSourceID, dataKindFilter(dataKind), dataKindFilter(dataKind)}, func() *pb.Dataset { return &pb.Dataset{} })
	if err != nil {
		return nil, nil, err
	}
	if freq != "" {
		filtered := items[:0]
		for _, item := range items {
			if containsString(item.GetFreqs(), freq) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return pageItems(items, page)
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
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_fields WHERE c_space_id = ? AND c_field_id = ?`, []any{spaceID, fieldID}, func() *pb.Field { return &pb.Field{} })
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

	statement := `SELECT c_attrs_json FROM t_fields WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + sortColumn + ` ` + direction + `, c_field_id ` + direction
	items, err := queryMessages(ctx, s.db, statement, args, func() *pb.Field { return &pb.Field{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, query.Page)
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
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_factors WHERE c_space_id = ? AND c_factor_id = ?`, []any{spaceID, factorID}, func() *pb.Factor { return &pb.Factor{} })
}

func (s *Store) ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_factors
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_algorithm = ?)
		ORDER BY c_space_id, c_factor_id
	`, []any{spaceID, spaceID, algorithm, algorithm}, func() *pb.Factor { return &pb.Factor{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, dataset_id and column_name are required")
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
		INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_is_unique, c_aliases_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_value_type = excluded.c_value_type,
			c_is_unique = excluded.c_is_unique,
			c_aliases_json = excluded.c_aliases_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName(), datasetOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), boolInt(item.GetIsUnique()), aliases, item.GetStatus(), raw)
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
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_dataset_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		ORDER BY c_space_id, c_dataset_id, c_column_name
	`, []any{spaceID, spaceID, datasetID, datasetID}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	return queryMessages(ctx, s.db, `SELECT c_attrs_json FROM t_views WHERE (? = '' OR c_space_id = ?) AND (? = '' OR c_primary_dataset_id = ? OR EXISTS (SELECT 1 FROM json_each(c_dataset_ids_json) ref WHERE ref.value = ?)) AND c_status = 'active' ORDER BY c_space_id, c_view_id`, []any{spaceID, spaceID, datasetID, datasetID, datasetID}, func() *pb.View { return &pb.View{} })
}
