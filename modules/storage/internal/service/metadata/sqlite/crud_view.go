//go:build legacy_metadata_view

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetName() == "" || item.GetPrimaryDatasetId() == "" {
		return nil, errors.New("space_id, view_id, name and primary_dataset_id are required")
	}
	columns := item.GetColumns()
	next := proto.Clone(item).(*pb.View)
	next.Columns = nil
	next.IndexBuild = nil
	next.Status = defaultStatus(next.GetStatus())
	if strings.TrimSpace(next.Engine) == "" {
		next.Engine = s.defaultViewEngine(ctx, next.GetSpaceId(), next.GetPrimaryDatasetId())
	} else {
		next.Engine = strings.ToLower(strings.TrimSpace(next.Engine))
	}
	if len(next.DatasetIds) == 0 {
		next.DatasetIds = []string{next.GetPrimaryDatasetId()}
	}
	datasetIDs, err := marshalJSON(next.GetDatasetIds())
	if err != nil {
		return nil, err
	}
	grainKeys, err := marshalJSON(next.GetGrainKeys())
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(next.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{item.GetSpaceId(), item.GetViewId()}, func() *pb.View { return &pb.View{} })
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	shapeChanged := existing != nil && viewIndexShapeChanged(existing, next)
	mergeViewIndexState(existing, next, shapeChanged)
	raw, err := marshal(next)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_description, c_primary_dataset_id, c_dataset_ids_json, c_grain_keys_json, c_filter_json, c_engine, c_retention_window, c_active_index_id, c_view_version, c_active_view_version, c_active_columns_json, c_active_view_schema_hash, c_indexed_from, c_indexed_to, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_description = excluded.c_description,
			c_primary_dataset_id = excluded.c_primary_dataset_id,
			c_dataset_ids_json = excluded.c_dataset_ids_json,
			c_grain_keys_json = excluded.c_grain_keys_json,
			c_filter_json = excluded.c_filter_json,
			c_engine = excluded.c_engine,
			c_retention_window = excluded.c_retention_window,
			c_active_index_id = excluded.c_active_index_id,
			c_view_version = excluded.c_view_version,
			c_active_view_version = excluded.c_active_view_version,
			c_active_columns_json = excluded.c_active_columns_json,
			c_active_view_schema_hash = excluded.c_active_view_schema_hash,
			c_indexed_from = excluded.c_indexed_from,
			c_indexed_to = excluded.c_indexed_to,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, next.GetSpaceId(), next.GetViewId(), next.GetName(), next.GetDescription(), next.GetPrimaryDatasetId(), datasetIDs, grainKeys, defaultJSON(next.GetFilterJson()), next.GetEngine(), next.GetRetentionWindow(), next.GetActiveIndexId(), next.GetViewVersion(), next.GetActiveViewVersion(), activeColumns, next.GetActiveViewSchemaHash(), next.GetIndexedFrom(), next.GetIndexedTo(), next.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	if shapeChanged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, next.GetSpaceId(), next.GetViewId()); err != nil {
			return nil, err
		}
	}
	columnsChanged := false
	for _, column := range columns {
		if column.GetSpaceId() == "" {
			column.SpaceId = next.GetSpaceId()
		}
		if column.GetViewId() == "" {
			column.ViewId = next.GetViewId()
		}
		if column.GetColumnName() != "" {
			changed, err := upsertViewColumn(ctx, tx, column)
			if err != nil {
				return nil, err
			}
			columnsChanged = columnsChanged || changed
		}
	}
	if existing != nil && columnsChanged && !shapeChanged {
		if err := bumpViewVersion(ctx, tx, next.GetSpaceId(), next.GetViewId()); err != nil {
			return nil, err
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

func (s *Store) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	view, err := getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{spaceID, viewID}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	columns, _, err := s.ListViewColumns(ctx, spaceID, viewID, nil)
	if err != nil {
		return nil, err
	}
	view.Columns = columns
	build, err := s.GetViewIndexBuild(ctx, spaceID, viewID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	view.IndexBuild = build
	return view, nil
}

func (s *Store) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	const where = `
		FROM t_views
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_status = ?)
		  AND (? = '' OR c_primary_dataset_id = ? OR EXISTS (
			  SELECT 1 FROM json_each(c_dataset_ids_json) AS dataset_ref WHERE dataset_ref.value = ?
		  ))`
	args := []any{spaceID, spaceID, status, status, datasetID, datasetID, datasetID}
	items, pageResult, err := queryPagedMessages(ctx, s.db,
		`SELECT c_attrs_json `+where+` ORDER BY c_space_id, c_view_id`,
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
		build, err := s.GetViewIndexBuild(ctx, item.GetSpaceId(), item.GetViewId())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		item.IndexBuild = build
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
	changed, err := upsertViewColumn(ctx, tx, item)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := bumpViewVersion(ctx, tx, item.GetSpaceId(), item.GetViewId()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func upsertViewColumn(ctx context.Context, store execQueryRower, item *pb.ViewColumn) (bool, error) {
	existing, err := getMessage(ctx, store, `SELECT c_attrs_json FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ? AND c_column_name = ?`, []any{item.GetSpaceId(), item.GetViewId(), item.GetColumnName()}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	raw, err := marshal(item)
	if err != nil {
		return false, err
	}
	_, err = store.ExecContext(ctx, `
		INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name, c_origin_type, c_origin_id, c_value_type, c_online_time, c_sort_order, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id, c_column_name) DO UPDATE SET
			c_origin_type = excluded.c_origin_type,
			c_origin_id = excluded.c_origin_id,
			c_value_type = excluded.c_value_type,
			c_online_time = excluded.c_online_time,
			c_sort_order = excluded.c_sort_order,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetViewId(), item.GetColumnName(), viewOriginSQL(item.GetOriginType()), item.GetOriginId(), valueTypeSQL(item.GetValueType()), item.GetOnlineTime(), item.GetSortOrder(), raw)
	if err != nil {
		return false, err
	}
	return existing == nil || viewColumnShapeChanged(existing, item), nil
}

func viewColumnShapeChanged(existing *pb.ViewColumn, next *pb.ViewColumn) bool {
	if existing.GetOriginType() != next.GetOriginType() {
		return true
	}
	if existing.GetOriginId() != next.GetOriginId() {
		return true
	}
	if existing.GetValueType() != next.GetValueType() {
		return true
	}
	if existing.GetOnlineTime() != next.GetOnlineTime() {
		return true
	}
	if existing.GetSortOrder() != next.GetSortOrder() {
		return true
	}
	return !mapsEqual(existing.GetAttributes(), next.GetAttributes())
}

func (s *Store) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_view_columns
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_view_id = ?)
		ORDER BY c_sort_order, c_column_name
	`, []any{spaceID, spaceID, viewID, viewID}, func() *pb.ViewColumn { return &pb.ViewColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
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
