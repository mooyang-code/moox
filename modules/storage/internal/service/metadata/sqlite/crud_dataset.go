package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

// UpsertDataset 创建或更新数据集。新建的数据集直接启用，columns 与数据集在同一事务内写入；已存在时逐列更新。
func (s *Store) UpsertDataset(ctx context.Context, item *pb.Dataset, columns ...*pb.DatasetColumn) (*pb.Dataset, error) {
	if item == nil {
		return nil, storeerr.Invalidf("dataset is required")
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
	nowTime := s.nowUTC()
	now := sqlTimestampText(nowTime)
	item.CreatedAt = nowTime.UnixMilli()
	item.UpdatedAt = nowTime.UnixMilli()
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
		return nil, storeerr.Invalidf("space_id, dataset_id and name are required")
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
			return nil, storeerr.Invalidf("dataset data_source_id is immutable")
		}
		dataSourceID = candidate
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_UNSPECIFIED && item.GetDataKind() != existing.GetDataKind() {
		return nil, storeerr.Invalidf("dataset data_kind is immutable")
	}
	if freq := strings.TrimSpace(item.GetFreq()); freq != "" && freq != existing.GetFreq() {
		return nil, storeerr.Invalidf("dataset freq is immutable")
	}
	// alignment 设置后不能修改，没有设置过的可以补上；请求里为空、allowed_lateness_seconds 为 0 时沿用已有值，
	// 只改名称、说明的更新不会把它们清掉。
	alignment := existing.GetAlignment()
	if candidate := strings.TrimSpace(item.GetAlignment()); candidate != "" && candidate != alignment {
		if alignment != "" {
			return nil, storeerr.Invalidf("数据集的 alignment 设置后不能修改")
		}
		alignment = candidate
	}
	item.Alignment = alignment
	if item.GetAllowedLatenessSeconds() == 0 {
		item.AllowedLatenessSeconds = existing.GetAllowedLatenessSeconds()
	}
	// maintenance_priority 同样按 0 沿用已有值：只改名称的更新不会把因子结果数据集的维护优先级清掉。
	if item.GetMaintenancePriority() == 0 {
		item.MaintenancePriority = existing.GetMaintenancePriority()
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
	updatedAt := s.nowUTC()
	item.UpdatedAt = updatedAt.UnixMilli()
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
	`, nullableDatasetSource(dataSourceID), item.GetName(), item.GetStatus(), raw, subjectTags, sqlTimestampText(updatedAt), item.GetSpaceId(), item.GetDatasetId(), existing.GetRevision())
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
	if status := strings.TrimSpace(query.Status); status != "" {
		where = append(where, "c_status = ?")
		args = append(args, status)
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
		return nil, storeerr.Invalidf("dataset is required")
	}
	item = proto.Clone(item).(*pb.Dataset)
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.DatasetId = strings.TrimSpace(item.GetDatasetId())
	item.DataSourceId = strings.TrimSpace(item.GetDataSourceId())
	item.Name = strings.TrimSpace(item.GetName())
	if item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetName() == "" {
		return nil, storeerr.Invalidf("space_id, dataset_id and name are required")
	}
	if item.GetDataKind() != pb.DataKind_DATA_KIND_RECORD && item.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return nil, storeerr.Invalidf("data_kind must be record or time_series")
	}
	// Retention is resolved from the Storage policy on read, never stored.
	item.Retention, item.RetentionSource = "", ""
	item.Freq = strings.TrimSpace(item.GetFreq())
	if item.GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES && !frequencypkg.IsCanonical(item.GetFreq()) {
		return nil, storeerr.Invalidf("time_series dataset freq must be one of %s: %q", strings.Join(frequencypkg.StandardStrings(), ", "), item.GetFreq())
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
		return storeerr.Invalidf("dataset status must be active or disabled: %q", status)
	}
	return nil
}

func getDatasetTx(ctx context.Context, tx queryRower, spaceID, datasetID string) (*pb.Dataset, error) {
	return scanMessageWithSQLTimestamps(tx.QueryRowContext(ctx, `
		SELECT c_item_json, c_ctime, c_mtime FROM t_datasets
		WHERE c_space_id = ? AND c_dataset_id = ?
	`, spaceID, datasetID), func() *pb.Dataset { return &pb.Dataset{} })
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

// upsertDatasetColumnTx 在事务内写入一列。已有的列只能改显示名、分组、状态、属性、必填与别名，类型不能改。
func upsertDatasetColumnTx(ctx context.Context, tx *sql.Tx, item *pb.DatasetColumn) (*pb.DatasetColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return nil, storeerr.Invalidf("space_id, dataset_id and column_name are required")
	}
	if item.GetValueType() == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
		return nil, storeerr.Invalidf("dataset column value_type must be declared")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.GetStatus() != "active" && item.GetStatus() != "disabled" {
		return nil, storeerr.Invalidf("数据集列的状态只能是 active 或 disabled：%q", item.GetStatus())
	}
	existing, getErr := getMessage(ctx, tx, `SELECT c_item_json FROM t_dataset_columns WHERE c_space_id = ? AND c_dataset_id = ? AND c_column_name = ?`,
		[]any{item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName()}, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	now := time.Now().UnixMilli()
	switch {
	case getErr == nil:
		if existing.GetValueType() != item.GetValueType() {
			return nil, storeerr.Invalidf("数据集列 %s 的类型不能修改", item.GetColumnName())
		}
		item.CreatedAt = existing.GetCreatedAt()
	case errors.Is(getErr, sql.ErrNoRows):
		item.CreatedAt = now
	default:
		return nil, getErr
	}
	item.UpdatedAt = now
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_dataset_columns (c_space_id, c_dataset_id, c_column_name, c_status, c_item_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_dataset_id, c_column_name) DO UPDATE SET
			c_status = excluded.c_status,
			c_item_json = excluded.c_item_json
	`, item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ListDatasetColumns 按建列顺序列出数据集的列（含停用的列）：View 的列就是其中启用的那些，顺序也按它。
func (s *Store) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	const where = `
		FROM t_dataset_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_item_json `+where+` ORDER BY c_space_id, c_dataset_id, c_id`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.DatasetColumn { return &pb.DatasetColumn{} },
	)
}
