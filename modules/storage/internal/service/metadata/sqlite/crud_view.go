package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	sqlite "modernc.org/sqlite"
)

const viewWithTimestampsSQL = `json_set(c_item_json, '$.created_at', c_ctime, '$.updated_at', c_mtime)`

// visibleDatasetColumnSQL 是数据集里启用的列：列本身启用，且它引用的字段（如果有）也启用。View 的列就是这些列。
const visibleDatasetColumnSQL = `dc.c_status = 'active' AND COALESCE(f.c_status, 'active') = 'active'`

// datasetColumnFieldJoinSQL 把数据集列连到它引用的字段，用来判断字段是否停用。
const datasetColumnFieldJoinSQL = `LEFT JOIN t_fields f ON dc.c_origin_type = 'field' AND f.c_space_id = dc.c_space_id AND f.c_field_id = dc.c_origin_id`

// UpsertView 写入 View 的声明；columns 由数据集列推出，写入时忽略。
func (s *Store) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	return s.upsertView(ctx, item, false)
}

// CreateView inserts a View only if its identity is unused. The INSERT's
// unique constraint is the atomic boundary shared by all Metadata clients.
func (s *Store) CreateView(ctx context.Context, item *pb.View) (*pb.View, error) {
	return s.upsertView(ctx, item, true)
}

// DeleteView physically removes a View. This is reserved for explicit
// lifecycle deletion; ordinary updates use status instead.
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

func (s *Store) upsertView(ctx context.Context, item *pb.View, createOnly bool) (*pb.View, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetName() == "" || item.GetDatasetId() == "" {
		return nil, storeerr.Invalidf("space_id, view_id, name and dataset_id are required")
	}
	next := proto.Clone(item).(*pb.View)
	next.Columns = nil
	next.HiddenColumns = nil
	next.Status = defaultStatus(next.GetStatus())
	if strings.TrimSpace(next.Engine) == "" {
		next.Engine = s.defaultViewEngine(ctx, next.GetSpaceId(), next.GetDatasetId())
	} else {
		next.Engine = strings.ToLower(strings.TrimSpace(next.Engine))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deriveViewFreq(ctx, tx, next); err != nil {
		return nil, err
	}
	if !createOnly {
		existing, err := getMessage(ctx, tx, `SELECT c_item_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{item.GetSpaceId(), item.GetViewId()}, func() *pb.View { return &pb.View{} })
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			if err := validateViewDefinitionChange(existing, next); err != nil {
				return nil, err
			}
		}
	}
	raw, err := marshal(next)
	if err != nil {
		return nil, err
	}
	insertSQL := `
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_dataset_id, c_engine, c_status, c_item_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	if !createOnly {
		insertSQL += `
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_dataset_id = excluded.c_dataset_id,
			c_engine = excluded.c_engine,
			c_status = excluded.c_status,
			c_item_json = excluded.c_item_json`
	}
	_, err = tx.ExecContext(ctx, insertSQL, next.GetSpaceId(), next.GetViewId(), next.GetName(), next.GetDatasetId(), next.GetEngine(), next.GetStatus(), raw)
	if err != nil {
		var sqliteErr *sqlite.Error
		if createOnly && errors.As(err, &sqliteErr) && (sqliteErr.Code() == 2067 || sqliteErr.Code() == 1555) {
			return nil, fmt.Errorf("%w: %s/%s", metadatastore.ErrViewExists, item.GetSpaceId(), item.GetViewId())
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, next.GetSpaceId(), next.GetViewId())
}

// validateViewDefinitionChange 拒绝建成后不能修改的定义：数据集、引擎、主键维度。要改就新建 View。
func validateViewDefinitionChange(existing, next *pb.View) error {
	if existing.GetDatasetId() != "" && existing.GetDatasetId() != next.GetDatasetId() {
		return storeerr.Invalidf("View 的数据集不能修改，请新建 View")
	}
	if existing.GetEngine() != "" && existing.GetEngine() != next.GetEngine() {
		return storeerr.Invalidf("View 的引擎不能修改，请新建 View")
	}
	if len(existing.GetGrainKeys()) > 0 && !slices.Equal(existing.GetGrainKeys(), next.GetGrainKeys()) {
		return storeerr.Invalidf("View 的主键维度不能修改，请新建 View")
	}
	return nil
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
		return storeerr.NotFoundf("dataset %s/%s does not exist", view.GetSpaceId(), view.GetDatasetId())
	}
	if err != nil {
		return err
	}
	if candidate := strings.TrimSpace(view.GetFreq()); candidate != "" && candidate != freq {
		return storeerr.Invalidf("view freq %q differs from dataset %s freq %q; a View takes its Dataset's freq", candidate, view.GetDatasetId(), freq)
	}
	view.Freq = freq
	return nil
}

func (s *Store) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	view, err := getMessage(ctx, s.queryDB(ctx), `SELECT `+viewWithTimestampsSQL+` FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	if err := s.fillViewColumns(ctx, view); err != nil {
		return nil, err
	}
	return view, nil
}

// fillViewColumns 按数据集列填上 View 的列与隐藏列。
func (s *Store) fillViewColumns(ctx context.Context, view *pb.View) error {
	columns, _, err := s.ListViewColumns(ctx, view.GetSpaceId(), view.GetViewId(), nil)
	if err != nil {
		return err
	}
	view.Columns = columns
	view.HiddenColumns, err = s.hiddenDatasetColumns(ctx, view.GetSpaceId(), view.GetDatasetId())
	return err
}

// hiddenDatasetColumns 返回数据集里逻辑上已删除的列：列本身被禁用，或它引用的字段不再启用。
func (s *Store) hiddenDatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	rows, err := s.queryDB(ctx).QueryContext(ctx, `
		SELECT dc.c_column_name
		FROM t_dataset_columns dc
		`+datasetColumnFieldJoinSQL+`
		WHERE dc.c_space_id = ? AND dc.c_dataset_id = ? AND NOT (`+visibleDatasetColumnSQL+`)
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
		if err := s.fillViewColumns(ctx, item); err != nil {
			return nil, nil, err
		}
	}
	return items, pageResult, nil
}

// ListViewColumns 由 View 所属数据集里启用的列推出 View 的列：按数据集列的创建顺序，sort_order 是这个顺序里的位置，
// 属性取自数据集列。view_id 为空时列出空间里所有 View 的列。
func (s *Store) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	const from = `
		FROM t_views v
		JOIN t_dataset_columns dc ON dc.c_space_id = v.c_space_id AND dc.c_dataset_id = v.c_dataset_id
		` + datasetColumnFieldJoinSQL + `
		WHERE (? = '' OR v.c_space_id = ?)
		  AND (? = '' OR v.c_view_id = ?)
		  AND ` + visibleDatasetColumnSQL
	args := []any{spaceID, spaceID, viewID, viewID}
	pageNo, size, offset := normalizePage(page)
	db := s.queryDB(ctx)
	rows, err := db.QueryContext(ctx, `
		SELECT v.c_space_id, v.c_view_id, dc.c_item_json,
			ROW_NUMBER() OVER (PARTITION BY v.c_space_id, v.c_view_id ORDER BY dc.c_id) - 1
		`+from+`
		ORDER BY v.c_space_id, v.c_view_id, dc.c_id
		LIMIT ? OFFSET ?`, append(append([]any{}, args...), int(size), offset)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []*pb.ViewColumn
	for rows.Next() {
		var columnSpaceID, columnViewID, raw string
		var order uint32
		if err := rows.Scan(&columnSpaceID, &columnViewID, &raw, &order); err != nil {
			return nil, nil, err
		}
		column := &pb.DatasetColumn{}
		if err := unmarshalOptions.Unmarshal([]byte(raw), column); err != nil {
			return nil, nil, err
		}
		items = append(items, &pb.ViewColumn{
			SpaceId: columnSpaceID, ViewId: columnViewID, ColumnName: column.GetColumnName(),
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: column.GetColumnName(),
			ValueType: column.GetValueType(), SortOrder: order,
			CreatedAt: column.GetCreatedAt(), UpdatedAt: column.GetUpdatedAt(), Attributes: column.GetAttributes(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	total, err := countRows(ctx, db, `SELECT COUNT(1) `+from, args)
	if err != nil {
		return nil, nil, err
	}
	hasMore := uint64(offset)+uint64(len(items)) < uint64(total)
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total, HasMore: hasMore}, nil
}
