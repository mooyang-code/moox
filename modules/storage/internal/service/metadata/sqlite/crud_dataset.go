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
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

// UpsertDataset 创建或更新数据集。新建的数据集直接启用，columns 与数据集在同一事务内写入；已存在时逐列更新。
func (s *Store) UpsertDataset(ctx context.Context, item *pb.Dataset, columns ...*pb.DatasetColumn) (*pb.Dataset, error) {
	if item == nil {
		return nil, errors.New("dataset is required")
	}
	if _, err := s.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err == nil {
		updated, err := s.UpdateDataset(ctx, item)
		if err != nil {
			return nil, err
		}
		for _, column := range columns {
			if _, err := s.UpsertDatasetColumn(ctx, datasetColumnOf(updated, column)); err != nil {
				return nil, err
			}
		}
		return updated, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.CreateDataset(ctx, item, columns...)
}

// datasetColumnOf 让列归属到给定数据集。
func datasetColumnOf(dataset *pb.Dataset, column *pb.DatasetColumn) *pb.DatasetColumn {
	out := proto.Clone(column).(*pb.DatasetColumn)
	out.SpaceId, out.DatasetId = dataset.GetSpaceId(), dataset.GetDatasetId()
	return out
}

func (s *Store) CreateDataset(ctx context.Context, item *pb.Dataset, columns ...*pb.DatasetColumn) (*pb.Dataset, error) {
	item, err := normalizeDatasetForCreate(item)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	item.SubjectTags = normalizeSubjectTags(item.GetSubjectTags())
	if err := validateSubjectTagsExist(ctx, tx, item.GetSpaceId(), item.GetSubjectTags()); err != nil {
		return nil, err
	}
	item.Status = "active"
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
			c_space_id, c_dataset_id, c_data_source_id, c_name, c_data_kind, c_freq,
			c_revision, c_status, c_item_json, c_subject_tags_json, c_ctime, c_mtime
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetDatasetId(), nullableDatasetSource(item.GetDataSourceId()), item.GetName(), dataKindSQL(item.GetDataKind()), item.GetFreq(), item.GetRevision(), item.GetStatus(), raw, subjectTags, now, now)
	if err != nil {
		return nil, err
	}
	for _, column := range columns {
		if _, err := upsertDatasetColumnTx(ctx, tx, datasetColumnOf(item, column)); err != nil {
			return nil, err
		}
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
		status = candidate
	}
	item.DataSourceId = dataSourceID
	item.DataKind = existing.GetDataKind()
	item.Freq = existing.GetFreq()
	item.Retention, item.RetentionSource = "", ""
	item.SubjectTags = normalizeSubjectTags(item.GetSubjectTags())
	if err := validateSubjectTagsExist(ctx, tx, item.GetSpaceId(), item.GetSubjectTags()); err != nil {
		return nil, err
	}
	item.Status = status
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
			c_data_source_id = ?, c_name = ?,
			c_status = ?, c_item_json = ?, c_subject_tags_json = ?, c_revision = c_revision + 1, c_mtime = ?
		WHERE c_space_id = ? AND c_dataset_id = ? AND c_revision = ?
	`, nullableDatasetSource(dataSourceID), item.GetName(), item.GetStatus(), raw, subjectTags, item.GetUpdatedAt(), item.GetSpaceId(), item.GetDatasetId(), existing.GetRevision())
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
		SELECT c_item_json, c_ctime, c_mtime FROM t_datasets
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

func (s *Store) GetDatasetColumn(ctx context.Context, spaceID string, datasetID string, columnName string) (*pb.DatasetColumn, error) {
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_item_json FROM t_dataset_columns WHERE c_space_id = ? AND c_dataset_id = ? AND c_column_name = ?`, []any{spaceID, datasetID, columnName}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
}

type DatasetQuery = coremetadata.DatasetQuery

func (s *Store) ListDatasets(ctx context.Context, query coremetadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	where := []string{"1 = 1"}
	args := make([]any, 0, 8)
	if spaceID := strings.TrimSpace(query.SpaceID); spaceID != "" {
		where = append(where, "c_space_id = ?")
		args = append(args, spaceID)
	}
	if dataSourceID := strings.TrimSpace(query.DataSourceID); dataSourceID != "" {
		where = append(where, "c_data_source_id = ?")
		args = append(args, dataSourceID)
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
		`SELECT c_item_json FROM t_datasets WHERE `+whereSQL+` ORDER BY c_space_id, c_dataset_id`,
		`SELECT COUNT(1) FROM t_datasets WHERE `+whereSQL,
		args, query.Page, func() *pb.Dataset { return &pb.Dataset{} },
	)
}

func normalizeDatasetForCreate(item *pb.Dataset) (*pb.Dataset, error) {
	if item == nil {
		return nil, errors.New("dataset is required")
	}
	item = proto.Clone(item).(*pb.Dataset)
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.DatasetId = strings.TrimSpace(item.GetDatasetId())
	item.DataSourceId = strings.TrimSpace(item.GetDataSourceId())
	item.Name = strings.TrimSpace(item.GetName())
	if item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetName() == "" {
		return nil, errors.New("space_id, dataset_id and name are required")
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
		SELECT c_item_json, c_ctime, c_mtime FROM t_datasets
		WHERE c_space_id = ? AND c_dataset_id = ?
	`, spaceID, datasetID), func() *pb.Dataset { return &pb.Dataset{} })
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
		INSERT INTO t_fields (c_space_id, c_field_id, c_group_id, c_name, c_description, c_value_type, c_sort_order, c_status, c_item_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_field_id) DO UPDATE SET
			c_group_id = excluded.c_group_id,
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_value_type = excluded.c_value_type,
			c_sort_order = excluded.c_sort_order,
			c_status = excluded.c_status,
			c_item_json = excluded.c_item_json
	`, item.GetSpaceId(), item.GetFieldId(), item.GetGroupId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetSortOrder(), item.GetStatus(), raw)
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
		INSERT INTO t_fields (c_space_id, c_field_id, c_group_id, c_name, c_description, c_value_type, c_sort_order, c_status, c_item_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.GetSpaceId(), item.GetFieldId(), item.GetGroupId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetSortOrder(), item.GetStatus(), raw)
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
		UPDATE t_fields SET c_group_id = ?, c_name = ?, c_description = ?, c_value_type = ?,
			c_sort_order = ?, c_status = ?, c_item_json = ?
		WHERE c_space_id = ? AND c_field_id = ?
	`, item.GetGroupId(), item.GetName(), item.GetDescription(), valueTypeSQL(item.GetValueType()), item.GetSortOrder(), item.GetStatus(), raw, item.GetSpaceId(), item.GetFieldId())
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
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_item_json FROM t_fields WHERE c_space_id = ? AND c_field_id = ?`, []any{spaceID, fieldID}, func() *pb.Field { return &pb.Field{} })
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
		`SELECT c_item_json FROM t_fields WHERE `+whereSQL+` ORDER BY `+sortColumn+` `+direction+`, c_field_id `+direction,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if item, err = upsertDatasetColumnTx(ctx, tx, item); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// upsertDatasetColumnTx 在事务内写入一列。View 的列由数据集列推出，不用同步。
func upsertDatasetColumnTx(ctx context.Context, tx *sql.Tx, item *pb.DatasetColumn) (*pb.DatasetColumn, error) {
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
	existing, getErr := getMessage(ctx, tx, `SELECT c_item_json FROM t_dataset_columns WHERE c_space_id = ? AND c_dataset_id = ? AND c_column_name = ?`,
		[]any{item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName()}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	if getErr == nil {
		if existing.GetOriginType() != item.GetOriginType() || existing.GetOriginId() != item.GetOriginId() || existing.GetValueType() != item.GetValueType() {
			return nil, errors.New("dataset column identity and value_type are immutable")
		}
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, getErr
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_origin_type, c_origin_id, c_status, c_item_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_status = excluded.c_status,
			c_item_json = excluded.c_item_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName(), datasetOriginSQL(item.GetOriginType()), item.GetOriginId(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Store) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	const where = `
		FROM t_dataset_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_item_json `+where+` ORDER BY c_space_id, c_dataset_id, c_column_name`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.DatasetColumn { return &pb.DatasetColumn{} },
	)
}

func (s *Store) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	return queryMessages(ctx, s.queryDB(ctx), `SELECT c_item_json FROM t_views WHERE (? = '' OR c_space_id = ?) AND (? = '' OR c_dataset_id = ?) AND c_status = 'active' ORDER BY c_space_id, c_view_id`, []any{spaceID, spaceID, datasetID, datasetID}, func() *pb.View { return &pb.View{} })
}
