package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// mergeViewIndexState 把已有 View 的索引状态带到这次更新里：修改 View 定义不能改动活动索引、已建列和覆盖范围。
func mergeViewIndexState(existing *pb.View, item *pb.View) {
	if existing == nil {
		return
	}
	item.ActiveIndexId = existing.GetActiveIndexId()
	item.ViewRevision = existing.GetViewRevision()
	item.ActiveColumns = cloneViewColumns(existing.GetActiveColumns())
	item.ActiveViewSchemaHash = existing.GetActiveViewSchemaHash()
	item.IndexedFrom = existing.GetIndexedFrom()
	item.IndexedTo = existing.GetIndexedTo()
	if item.Attributes == nil {
		item.Attributes = make(map[string]string)
	}
	if value := existing.GetAttributes()["moox.columns_explicit"]; value != "" && item.Attributes["moox.columns_explicit"] == "" {
		item.Attributes["moox.columns_explicit"] = value
	}
	// 索引代次只在建成时写入：修改 View 定义不能清掉或伪造它。
	if value := existing.GetAttributes()[metadatastore.IndexGenerationAttribute]; value != "" {
		item.Attributes[metadatastore.IndexGenerationAttribute] = value
	} else {
		delete(item.Attributes, metadatastore.IndexGenerationAttribute)
	}
}

// validateViewDefinitionChange 拒绝建成后不能修改的定义：数据集、引擎、主键维度。要改就新建 View。
func validateViewDefinitionChange(existing, next *pb.View) error {
	if existing.GetDatasetId() != "" && existing.GetDatasetId() != next.GetDatasetId() {
		return errors.New("View 的数据集不能修改，请新建 View")
	}
	if existing.GetEngine() != "" && existing.GetEngine() != next.GetEngine() {
		return errors.New("View 的引擎不能修改，请新建 View")
	}
	if len(existing.GetGrainKeys()) > 0 && !slices.Equal(existing.GetGrainKeys(), next.GetGrainKeys()) {
		return errors.New("View 的主键维度不能修改，请新建 View")
	}
	return nil
}

// CompleteViewInitialBuild 在首次建索引（含回填）完成后写入活动索引与已建列，View 从此可读。
// 响应丢失后重试是幂等的；已经建成另一个索引时返回冲突。
func (s *Store) CompleteViewInitialBuild(ctx context.Context, req *pb.CompleteViewInitialBuildReq) (*pb.View, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetIndexId() == "" || req.GetViewSchemaHash() == "" {
		return nil, errors.New("space_id、view_id、index_id 和 view_schema_hash 不能为空")
	}
	ref, err := coreviewindex.ParseViewIndexID(req.GetIndexId())
	if err != nil {
		return nil, err
	}
	if ref.SpaceID != req.GetSpaceId() || ref.ViewID != req.GetViewId() {
		return nil, errors.New("index_id 与 space_id/view_id 不一致")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{req.GetSpaceId(), req.GetViewId()}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	if view.GetActiveIndexId() != "" {
		if view.GetActiveIndexId() == req.GetIndexId() && view.GetActiveViewSchemaHash() == req.GetViewSchemaHash() {
			_ = tx.Rollback()
			return s.GetView(ctx, req.GetSpaceId(), req.GetViewId())
		}
		return nil, ErrViewIndexConflict
	}
	wantHash := coreviewindex.HashViewIndexSchema(coreviewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetDatasetId(), Engine: view.GetEngine(), Columns: req.GetColumns(),
	})
	if wantHash != req.GetViewSchemaHash() {
		return nil, errors.New("view_schema_hash 与已建列不一致")
	}
	view.ActiveIndexId = req.GetIndexId()
	view.ViewRevision = 1
	view.ActiveColumns = cloneViewColumns(req.GetColumns())
	view.ActiveViewSchemaHash = wantHash
	if view.Attributes == nil {
		view.Attributes = make(map[string]string)
	}
	view.Attributes[metadatastore.IndexGenerationAttribute] = s.nowUTC().Format(sqliteBuildTimestampLayout)
	view.Columns = nil
	raw, err := marshal(view)
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(view.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_active_index_id = ?, c_view_revision = ?, c_active_columns_json = ?,
			c_active_view_schema_hash = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_active_index_id = ''
	`, view.GetActiveIndexId(), view.GetViewRevision(), activeColumns, view.GetActiveViewSchemaHash(), raw, view.GetSpaceId(), view.GetViewId())
	if err != nil {
		return nil, err
	}
	if err := requireChangedRow(res); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, req.GetSpaceId(), req.GetViewId())
}

// CommitViewSchemaExtension 在索引原地追加列之后提交新的已建列，修订号加 1。响应丢失后重试是幂等的。
func (s *Store) CommitViewSchemaExtension(ctx context.Context, req *pb.CommitViewSchemaExtensionReq) (*pb.View, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetViewId() == "" || req.GetActiveIndexId() == "" ||
		req.GetExpectedViewRevision() == 0 || req.GetExpectedSchemaHash() == "" || req.GetViewSchemaHash() == "" || len(req.GetColumns()) == 0 {
		return nil, errors.New("加列提交缺少索引身份或列")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	view, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, []any{req.GetSpaceId(), req.GetViewId()}, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	if view.GetViewRevision() == req.GetExpectedViewRevision()+1 && view.GetActiveIndexId() == req.GetActiveIndexId() &&
		view.GetActiveViewSchemaHash() == req.GetViewSchemaHash() &&
		proto.Equal(&pb.View{ActiveColumns: view.GetActiveColumns()}, &pb.View{ActiveColumns: req.GetColumns()}) {
		_ = tx.Rollback()
		return s.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	}
	if view.GetActiveIndexId() != req.GetActiveIndexId() || view.GetViewRevision() != req.GetExpectedViewRevision() ||
		view.GetActiveViewSchemaHash() != req.GetExpectedSchemaHash() {
		return nil, metadatastore.ErrViewSchemaExtensionConflict
	}
	if !coreviewindex.IsAppendOnlyViewColumns(view.GetActiveColumns(), req.GetColumns()) {
		return nil, metadatastore.ErrViewSchemaExtensionConflict
	}
	wantHash := coreviewindex.HashViewIndexSchema(coreviewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetDatasetId(), Engine: view.GetEngine(), Columns: req.GetColumns(),
	})
	if wantHash != req.GetViewSchemaHash() {
		return nil, errors.New("view_schema_hash 与已建列不一致")
	}
	view.ViewRevision = req.GetExpectedViewRevision() + 1
	view.ActiveColumns = cloneViewColumns(req.GetColumns())
	view.ActiveViewSchemaHash = wantHash
	view.Columns = nil
	raw, err := marshal(view)
	if err != nil {
		return nil, err
	}
	activeColumns, err := marshalJSON(view.GetActiveColumns())
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE t_views SET c_view_revision = ?, c_active_columns_json = ?, c_active_view_schema_hash = ?, c_attrs_json = ?
		WHERE c_space_id = ? AND c_view_id = ? AND c_active_index_id = ? AND c_view_revision = ? AND c_active_view_schema_hash = ?
	`, view.GetViewRevision(), activeColumns, view.GetActiveViewSchemaHash(), raw,
		req.GetSpaceId(), req.GetViewId(), req.GetActiveIndexId(), req.GetExpectedViewRevision(), req.GetExpectedSchemaHash())
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, metadatastore.ErrViewSchemaExtensionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetView(ctx, req.GetSpaceId(), req.GetViewId())
}

// rejectViewColumnRemoval 拒绝删除 View 的列：列只能追加，声明里的列和已经建进索引的列都必须保留，已建列的来源和类型也不能变。
func rejectViewColumnRemoval(ctx context.Context, tx *sql.Tx, view *pb.View, columns []*pb.ViewColumn) error {
	keep := make(map[string]*pb.ViewColumn, len(columns))
	for _, column := range columns {
		if column != nil && column.GetColumnName() != "" {
			keep[column.GetColumnName()] = column
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT c_column_name FROM t_view_columns WHERE c_space_id = ? AND c_view_id = ? ORDER BY c_column_name`, view.GetSpaceId(), view.GetViewId())
	if err != nil {
		return err
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if _, ok := keep[name]; !ok {
			missing = append(missing, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, active := range view.GetActiveColumns() {
		column, ok := keep[active.GetColumnName()]
		if !ok {
			if !slices.Contains(missing, active.GetColumnName()) {
				missing = append(missing, active.GetColumnName())
			}
			continue
		}
		if column.GetOriginType() != active.GetOriginType() || column.GetOriginId() != active.GetOriginId() || column.GetValueType() != active.GetValueType() {
			return fmt.Errorf("View 列 %q 的来源和类型不能修改", active.GetColumnName())
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("View 的列只能追加，不能删除：%s", strings.Join(missing, ", "))
	}
	return nil
}
