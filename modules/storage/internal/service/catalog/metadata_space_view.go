package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 本文件聚合 Space 与 View（含 ViewColumn）相关的元数据 CRUD 入口。

func (s *Service) CreateSpace(ctx context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || (space.GetSpaceId() == "" && space.GetName() == "") {
		return &pb.CreateSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id or name is required"))}, nil
	}
	if space.SpaceId == "" {
		space.SpaceId = defaultID(space.GetName(), "space")
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	created, err := s.metadata.CreateSpace(ctx, space)
	if err != nil {
		return &pb.CreateSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	// The SQLite INSERT is already committed before this point. Cache
	// publication is best-effort so a response retry cannot turn a successful
	// create into a duplicate-key failure or leak an orphaned Space.
	s.refreshMetadataCacheAfterCommit(ctx, "CreateSpace")
	return &pb.CreateSpaceRsp{RetInfo: retinfo.Success("success"), Space: created}, nil
}

func (s *Service) UpdateSpace(ctx context.Context, req *pb.UpdateSpaceReq) (*pb.UpdateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || space.GetSpaceId() == "" {
		return &pb.UpdateSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	updated, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateSpaceRsp{RetInfo: retinfo.Success("success"), Space: updated}, nil
}

func (s *Service) DeleteSpace(ctx context.Context, req *pb.DeleteSpaceReq) (*pb.DeleteSpaceRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	if err := s.metadata.DeleteSpace(ctx, req.GetSpaceId()); err != nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "DeleteSpace")
	return &pb.DeleteSpaceRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) GetSpace(ctx context.Context, req *pb.GetSpaceReq) (*pb.GetSpaceRsp, error) {
	space, err := s.metadata.GetSpace(ctx, req.GetSpaceId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &pb.GetSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_SPACE_NOT_FOUND, err)}, nil
		}
		return &pb.GetSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetSpaceRsp{RetInfo: retinfo.Success("success"), Space: space}, nil
}

func (s *Service) ListSpaces(ctx context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	items, page, err := s.metadata.ListSpaces(ctx, req.GetOwner(), req.GetPage())
	if err != nil {
		return &pb.ListSpacesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListSpacesRsp{RetInfo: retinfo.Success("success"), Spaces: items, PageResult: page}, nil
}

func (s *Service) CreateView(ctx context.Context, req *pb.CreateViewReq) (*pb.CreateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || (view.GetViewId() == "" && view.GetName() == "") {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id or name are required"))}, nil
	}
	if view.ViewId == "" {
		view.ViewId = "view_" + defaultID(view.GetName(), "view")
	}
	if err := validateChineseDisplayName("view name", view.GetName()); err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewID(view.GetViewId()); err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewColumns(view.GetColumns()); err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	clearViewIndexRuntimeState(view)
	var created *pb.View
	var err error
	if req.GetCreateOnly() {
		created, err = s.metadata.CreateView(ctx, view)
		if errors.Is(err, metadatastore.ErrViewExists) {
			return &pb.CreateViewRsp{RetInfo: retinfo.Error(pb.ErrorCode_CONFLICT, err)}, nil
		}
	} else {
		created, err = s.metadata.UpsertView(ctx, view)
	}
	if err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateViewRsp{RetInfo: retinfo.Success("success"), View: created}, nil
}

func (s *Service) UpdateView(ctx context.Context, req *pb.UpdateViewReq) (*pb.UpdateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	if view.Name == "" {
		view.Name = view.GetViewId()
	}
	if err := validateChineseDisplayName("view name", view.GetName()); err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewID(view.GetViewId()); err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewColumns(view.GetColumns()); err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	clearViewIndexRuntimeState(view)
	var updated *pb.View
	var err error
	if req.GetReplaceColumns() {
		updated, err = s.metadata.ReplaceViewColumns(ctx, view)
	} else {
		updated, err = s.metadata.UpsertView(ctx, view)
	}
	if err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateViewRsp{RetInfo: retinfo.Success("success"), View: updated}, nil
}

func (s *Service) DeleteView(ctx context.Context, req *pb.DeleteViewReq) (*pb.DeleteViewRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetViewId()) == "" {
		return &pb.DeleteViewRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	if err := s.metadata.DeleteView(ctx, req.GetSpaceId(), req.GetViewId()); err != nil {
		return &pb.DeleteViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "DeleteView")
	return &pb.DeleteViewRsp{RetInfo: retinfo.Success("success")}, nil
}

func clearViewIndexRuntimeState(view *pb.View) {
	if view == nil {
		return
	}
	view.ActiveIndexId = ""
	view.ViewRevision = 0
	view.ActiveColumns = nil
	view.ActiveViewSchemaHash = ""
	view.IndexedFrom = ""
	view.IndexedTo = ""
}

func (s *Service) normalizeAndValidateViewDatasets(ctx context.Context, view *pb.View) error {
	if view == nil {
		return errors.New("view is required")
	}
	spaceID := strings.TrimSpace(view.GetSpaceId())
	datasetID := viewDatasetID(view)
	if spaceID == "" || datasetID == "" {
		return errors.New("space_id and dataset_id are required")
	}
	dataset, err := s.metadata.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("view dataset %s not found: %w", datasetID, err)
	}
	existing, err := s.metadata.GetView(ctx, spaceID, view.GetViewId())
	if err != nil {
		existing = nil // 新建 View 时还不存在，所有列都按新列校验
	}
	if err := s.validateViewColumnsInDataset(ctx, spaceID, datasetID, newViewColumns(existing, view.GetColumns())); err != nil {
		return err
	}
	if freq := strings.TrimSpace(view.GetFreq()); freq != "" && freq != dataset.GetFreq() {
		return fmt.Errorf("view freq %q differs from dataset %s freq %q; a View takes its Dataset's freq", freq, dataset.GetDatasetId(), dataset.GetFreq())
	}
	view.Freq = dataset.GetFreq()
	view.DatasetId = datasetID
	view.GrainKeys = defaultViewGrainKeys(dataset.GetDataKind())
	view.Engine = defaultViewEngine(dataset.GetDataKind())
	return nil
}

// newViewColumns 去掉 View 已有的列：列只能追加，数据集列被禁用后 View 里的这一列也要留着，所以只校验新列。
func newViewColumns(view *pb.View, columns []*pb.ViewColumn) []*pb.ViewColumn {
	existing := make(map[string]struct{}, len(view.GetColumns())+len(view.GetActiveColumns()))
	for _, column := range append(append([]*pb.ViewColumn(nil), view.GetColumns()...), view.GetActiveColumns()...) {
		existing[column.GetColumnName()] = struct{}{}
	}
	added := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if _, ok := existing[column.GetColumnName()]; !ok {
			added = append(added, column)
		}
	}
	return added
}

// validateViewColumnsInDataset requires every dataset column of a View to
// name an active column of the View's Dataset. View column names carry no
// Dataset prefix, so this is what binds a column to its source.
func (s *Service) validateViewColumnsInDataset(ctx context.Context, spaceID, datasetID string, columns []*pb.ViewColumn) error {
	var wanted []string
	for _, column := range columns {
		if isDatasetViewColumn(column) {
			wanted = append(wanted, strings.TrimSpace(column.GetOriginId()))
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	active := make(map[string]struct{})
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := s.metadata.ListDatasetColumns(ctx, spaceID, datasetID, &pb.Page{Page: pageNo, Size: 1000})
		if err != nil {
			return fmt.Errorf("list view dataset %s columns: %w", datasetID, err)
		}
		for _, item := range items {
			if item != nil && (item.GetStatus() == "" || item.GetStatus() == "active") {
				active[item.GetColumnName()] = struct{}{}
			}
		}
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			break
		}
	}
	for _, name := range wanted {
		if _, ok := active[name]; !ok {
			return fmt.Errorf("view column %q is not an active column of dataset %s", name, datasetID)
		}
	}
	return nil
}

func (s *Service) GetView(ctx context.Context, req *pb.GetViewReq) (*pb.GetViewRsp, error) {
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.GetViewRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	return &pb.GetViewRsp{RetInfo: retinfo.Success("success"), View: view}, nil
}

func (s *Service) ListViews(ctx context.Context, req *pb.ListViewsReq) (*pb.ListViewsRsp, error) {
	items, page, err := s.metadata.ListViews(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetStatus(), req.GetPage())
	if err != nil {
		return &pb.ListViewsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListViewsRsp{RetInfo: retinfo.Success("success"), Views: items, PageResult: page}, nil
}

func (s *Service) UpsertViewColumn(ctx context.Context, req *pb.UpsertViewColumnReq) (*pb.UpsertViewColumnRsp, error) {
	column := req.GetColumn()
	if column == nil || column.GetSpaceId() == "" || column.GetViewId() == "" || column.GetColumnName() == "" {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, view_id and column_name are required"))}, nil
	}
	if err := validateColumnDisplayName("view column display_name", column.GetAttributes()); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewColumnName(column); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	view, err := s.metadata.GetView(ctx, column.GetSpaceId(), column.GetViewId())
	if err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.validateViewColumnsInDataset(ctx, column.GetSpaceId(), view.GetDatasetId(), newViewColumns(view, []*pb.ViewColumn{column})); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertViewColumn(ctx, column)
	if err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertViewColumnRsp{RetInfo: retinfo.Success("success"), Column: created}, nil
}

func (s *Service) ListViewColumns(ctx context.Context, req *pb.ListViewColumnsReq) (*pb.ListViewColumnsRsp, error) {
	items, page, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), req.GetPage())
	if err != nil {
		return &pb.ListViewColumnsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if req.GetViewId() != "" {
		view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
		if err != nil {
			return &pb.ListViewColumnsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
		}
		items = visibleViewColumns(items, view.GetHiddenColumns())
	}
	return &pb.ListViewColumnsRsp{RetInfo: retinfo.Success("success"), Columns: items, PageResult: page}, nil
}

// visibleViewColumns 去掉逻辑上已删除的列。
func visibleViewColumns(columns []*pb.ViewColumn, hidden []string) []*pb.ViewColumn {
	if len(hidden) == 0 {
		return columns
	}
	visible := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if !slices.Contains(hidden, column.GetColumnName()) {
			visible = append(visible, column)
		}
	}
	return visible
}
