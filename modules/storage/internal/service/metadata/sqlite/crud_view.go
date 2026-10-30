package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	sqlite "modernc.org/sqlite"
)

const viewWithTimestampsSQL = `json_set(c_attrs_json, '$.created_at', c_ctime, '$.updated_at', c_mtime)`

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	// UpsertView is the ordinary metadata update path. A protobuf repeated
	// field has no portable presence bit, so a non-empty partial columns list
	// must never be interpreted as a complete replacement. Callers that own the
	// full desired schema use ReplaceViewColumns explicitly.
	return s.upsertView(ctx, item, false, false)
}

// CreateView inserts a View only if its identity is unused. The INSERT's
// unique constraint is the atomic boundary shared by all Metadata clients.
func (s *Store) CreateView(ctx context.Context, item *pb.View) (*pb.View, error) {
	return s.upsertView(ctx, item, false, true)
}

// ReplaceViewColumns applies a complete desired column set, including an
// intentionally empty set. It is used by lifecycle cleanup because proto3
// repeated fields cannot preserve the distinction between omitted and empty
// across an RPC boundary.
func (s *Store) ReplaceViewColumns(ctx context.Context, item *pb.View) (*pb.View, error) {
	return s.upsertView(ctx, item, true, false)
}

// DeleteView physically removes a View and all dependent metadata rows via
// the schema's foreign-key cascades. This is reserved for explicit lifecycle
// deletion; ordinary updates use status instead.
func (s *Store) DeleteView(ctx context.Context, spaceID, viewID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, strings.TrimSpace(spaceID), strings.TrimSpace(viewID))
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

func (s *Store) upsertView(ctx context.Context, item *pb.View, replaceColumns, createOnly bool) (*pb.View, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetName() == "" || item.GetDatasetId() == "" {
		return nil, errors.New("space_id, view_id, name and dataset_id are required")
	}
	columns := item.GetColumns()
	next := proto.Clone(item).(*pb.View)
	if replaceColumns {
		// proto3 repeated fields do not carry presence. Persist the explicit
		// replacement intent so an intentionally empty projection is not later
		// expanded to the primary dataset's default columns by the View Maintainer.
		if next.Attributes == nil {
			next.Attributes = make(map[string]string)
		}
		next.Attributes["moox.columns_explicit"] = "true"
	}
	next.Columns = nil
	next.HiddenColumns = nil
	next.Status = defaultStatus(next.GetStatus())
	if strings.TrimSpace(next.Engine) == "" {
		next.Engine = s.defaultViewEngine(ctx, next.GetSpaceId(), next.GetDatasetId())
	} else {
		next.Engine = strings.ToLower(strings.TrimSpace(next.Engine))
	}
	if err := validateSingleViewDataset(next.GetDatasetId()); err != nil {
		return nil, err
	}
	grainKeys, err := marshalJSON(next.GetGrainKeys())
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deriveViewFreq(ctx, tx, next); err != nil {
		return nil, err
	}
	var existing *pb.View
	if !createOnly {
		existing, err = getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{item.GetSpaceId(), item.GetViewId()}, func() *pb.View { return &pb.View{} })
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if existing != nil {
		if err := validateViewDefinitionChange(existing, next); err != nil {
			return nil, err
		}
	}
	mergeViewIndexState(existing, next)
	raw, err := marshal(next)
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(next.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	insertSQL := `
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_description, c_dataset_id, c_grain_keys_json, c_engine, c_active_index_id, c_view_revision, c_active_columns_json, c_active_view_schema_hash, c_indexed_from, c_indexed_to, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if !createOnly {
		insertSQL += `
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_dataset_id = excluded.c_dataset_id,
			c_grain_keys_json = excluded.c_grain_keys_json,
			c_engine = excluded.c_engine,
			c_active_index_id = excluded.c_active_index_id,
			c_view_revision = excluded.c_view_revision,
			c_active_columns_json = excluded.c_active_columns_json,
			c_active_view_schema_hash = excluded.c_active_view_schema_hash,
			c_indexed_from = excluded.c_indexed_from,
			c_indexed_to = excluded.c_indexed_to,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json`
	}
	_, err = tx.ExecContext(ctx, insertSQL, next.GetSpaceId(), next.GetViewId(), next.GetName(), next.GetDescription(), next.GetDatasetId(), grainKeys, next.GetEngine(), next.GetActiveIndexId(), next.GetViewRevision(), activeColumns, next.GetActiveViewSchemaHash(), next.GetIndexedFrom(), next.GetIndexedTo(), next.GetStatus(), raw)
	if err != nil {
		var sqliteErr *sqlite.Error
		if createOnly && errors.As(err, &sqliteErr) && (sqliteErr.Code() == 2067 || sqliteErr.Code() == 1555) {
			return nil, fmt.Errorf("%w: %s/%s", metadatastore.ErrViewExists, item.GetSpaceId(), item.GetViewId())
		}
		return nil, err
	}
	if replaceColumns {
		if err := rejectViewColumnRemoval(ctx, tx, next, columns); err != nil {
			return nil, err
		}
	}
	for _, column := range columns {
		if column == nil {
			continue
		}
		// 子列必须归属父 View：非空且不一致的身份不能被写入，否则会改动另一个 View 且不更新其版本
		if column.GetSpaceId() != "" && column.GetSpaceId() != next.GetSpaceId() {
			return nil, fmt.Errorf("视图列 %q 的 space_id %q 与视图 %q 不一致", column.GetColumnName(), column.GetSpaceId(), next.GetSpaceId())
		}
		if column.GetViewId() != "" && column.GetViewId() != next.GetViewId() {
			return nil, fmt.Errorf("视图列 %q 的 view_id %q 与视图 %q 不一致", column.GetColumnName(), column.GetViewId(), next.GetViewId())
		}
		column.SpaceId = next.GetSpaceId()
		column.ViewId = next.GetViewId()
		if column.GetColumnName() != "" {
			if err := upsertViewColumn(ctx, tx, column); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, next.GetSpaceId(), next.GetViewId())
}

func (s *Store) defaultViewEngine(ctx context.Context, spaceID string, datasetID string) string {
	dataset, err := s.GetDataset(ctx, spaceID, datasetID)
	if err == nil && dataset.GetDataKind() == pb.DataKind_DATA_KIND_RECORD {
		return "bleve"
	}
	return "duckdb"
}

// deriveViewFreq copies the Dataset frequency onto the View. A View has no
// frequency of its own; a caller-supplied value must match the Dataset's.
func deriveViewFreq(ctx context.Context, tx *sql.Tx, view *pb.View) error {
	var freq string
	err := tx.QueryRowContext(ctx, `SELECT c_freq FROM t_datasets WHERE c_space_id = ? AND c_dataset_id = ?`,
		view.GetSpaceId(), view.GetDatasetId()).Scan(&freq)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("dataset %s/%s does not exist", view.GetSpaceId(), view.GetDatasetId())
	}
	if err != nil {
		return err
	}
	if candidate := strings.TrimSpace(view.GetFreq()); candidate != "" && candidate != freq {
		return fmt.Errorf("view freq %q differs from dataset %s freq %q; a View takes its Dataset's freq", candidate, view.GetDatasetId(), freq)
	}
	view.Freq = freq
	return nil
}

func (s *Store) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	view, err := getMessage(ctx, s.queryDB(ctx), `SELECT `+viewWithTimestampsSQL+` FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	columns, _, err := s.ListViewColumns(ctx, spaceID, viewID, nil)
	if err != nil {
		return nil, err
	}
	view.Columns = columns
	if view.HiddenColumns, err = s.hiddenDatasetColumns(ctx, spaceID, view.GetDatasetId()); err != nil {
		return nil, err
	}
	return view, nil
}

// hiddenDatasetColumns 返回数据集里逻辑上已删除的列：列本身被禁用，或它引用的字段不再启用。
func (s *Store) hiddenDatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	rows, err := s.queryDB(ctx).QueryContext(ctx, `
		SELECT dc.c_column_name
		FROM t_dataset_columns dc
		LEFT JOIN t_fields f ON dc.c_origin_type = 'field' AND f.c_space_id = dc.c_space_id AND f.c_field_id = dc.c_origin_id
		WHERE dc.c_space_id = ? AND dc.c_dataset_id = ?
		  AND (dc.c_status <> 'active' OR COALESCE(f.c_status, 'active') <> 'active')
		ORDER BY dc.c_column_name`, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hidden []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		hidden = append(hidden, name)
	}
	return hidden, rows.Err()
}

func (s *Store) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	const where = `
		FROM t_views
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_status = ?)
		  AND (? = '' OR c_dataset_id = ?)`
	args := []any{spaceID, spaceID, status, status, datasetID, datasetID}
	items, pageResult, err := queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT `+viewWithTimestampsSQL+` `+where+` ORDER BY c_space_id, c_view_id`,
		`SELECT COUNT(1) `+where,
		args,
		page,
		func() *pb.View { return &pb.View{} },
	)
	if err != nil {
		return nil, nil, err
	}
	for _, item := range items {
		columns, _, err := s.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), nil)
		if err != nil {
			return nil, nil, err
		}
		item.Columns = columns
		if item.HiddenColumns, err = s.hiddenDatasetColumns(ctx, item.GetSpaceId(), item.GetDatasetId()); err != nil {
			return nil, nil, err
		}
	}
	return items, pageResult, nil
}

func cloneViewColumns(columns []*pb.ViewColumn) []*pb.ViewColumn {
	out := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			out = append(out, proto.Clone(column).(*pb.ViewColumn))
		}
	}
	return out
}

func (s *Store) UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetColumnName() == "" {
		return nil, errors.New("space_id, view_id and column_name are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertViewColumn(ctx, tx, item); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// upsertViewColumn 写入 View 列：已有列的来源和类型不能改、排序保持原值，只有属性和上线时间可改；新列排在现有列之后。
func upsertViewColumn(ctx context.Context, store execQueryRower, item *pb.ViewColumn) error {
	existing, err := getMessage(ctx, store, `SELECT c_attrs_json FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ? AND c_column_name = ?`, []any{item.GetSpaceId(), item.GetViewId(), item.GetColumnName()}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
	switch {
	case err == nil:
		if existing.GetOriginType() != item.GetOriginType() || existing.GetOriginId() != item.GetOriginId() || existing.GetValueType() != item.GetValueType() {
			return fmt.Errorf("View 列 %q 的来源和类型不能修改", item.GetColumnName())
		}
		item.SortOrder = existing.GetSortOrder()
	case errors.Is(err, sql.ErrNoRows):
		var nextOrder uint32
		if err := store.QueryRowContext(ctx, `SELECT COALESCE(MAX(c_sort_order) + 1, 0) FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ?`, item.GetSpaceId(), item.GetViewId()).Scan(&nextOrder); err != nil {
			return err
		}
		item.SortOrder = max(item.GetSortOrder(), nextOrder)
	default:
		return err
	}
	raw, err := marshal(item)
	if err != nil {
		return err
	}
	_, err = store.ExecContext(ctx, `
		INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_online_time, c_sort_order, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id, c_column_name) DO UPDATE SET
			c_online_time = excluded.c_online_time,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetViewId(), item.GetColumnName(), viewOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), item.GetOnlineTime(), item.GetSortOrder(), raw)
	return err
}

func (s *Store) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	const where = `
		FROM t_view_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_view_id = ?)`
	args := []any{spaceID, spaceID, viewID, viewID}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_sort_order, c_column_name`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.ViewColumn { return &pb.ViewColumn{} },
	)
}

func viewOriginSQL(origin pb.ColumnOriginType) string {
	switch origin {
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION:
		return "expression"
	case pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM:
		return "system"
	default:
		return "dataset_column"
	}
}

func validateSingleViewDataset(datasetID string) error {
	if strings.TrimSpace(datasetID) == "" {
		return errors.New("dataset_id is required")
	}
	return nil
}
