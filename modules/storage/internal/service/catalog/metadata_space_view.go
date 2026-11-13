package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 本文件聚合 Space 与 View 相关的元数据 CRUD 入口。View 的列由数据集列推出，不能单独写。

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
	if s.rows == nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("dataset row store is not configured"))}, nil
	}
	// 和 DeleteDataset 一样服务端一次做完：先记下空间里的数据集，删元数据（级联删数据集、View 等）并发布缓存，
	// 再在写锁里逐个删行。View 的索引交给 View 的退役索引清理。
	datasetIDs, err := s.spaceDatasetIDs(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.metadata.DeleteSpace(ctx, req.GetSpaceId()); err != nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCacheSynchronously(ctx, "DeleteSpace"); err != nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("空间元数据已删除，但缓存尚未发布，请对这些数据集逐个重试 DeleteDataset 以删除行：%s", strings.Join(datasetIDs, ", ")))}, nil
	}
	for index, datasetID := range datasetIDs {
		if _, err := s.rows.DeleteDatasetRows(ctx, req.GetSpaceId(), datasetID); err != nil {
			// 元数据已不在时 DeleteDataset 照样删行，所以剩下的数据集可以逐个重试。
			return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("空间元数据已删除，删行失败（%w），请对这些数据集逐个重试 DeleteDataset：%s", err, strings.Join(datasetIDs[index:], ", ")))}, nil
		}
	}
	return &pb.DeleteSpaceRsp{RetInfo: retinfo.Success("success")}, nil
}

// spaceDatasetIDs 列出空间里全部数据集的 ID。
func (s *Service) spaceDatasetIDs(ctx context.Context, spaceID string) ([]string, error) {
	const pageSize = 1000
	ids := make([]string, 0)
	for page := uint32(1); ; page++ {
		items, result, err := s.metadata.ListDatasets(ctx, metadatastore.DatasetQuery{SpaceID: spaceID, Page: &pb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			ids = append(ids, item.GetDatasetId())
		}
		if result == nil || !result.GetHasMore() {
			return ids, nil
		}
	}
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
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.CreateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
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
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.UpdateViewRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	updated, err := s.metadata.UpsertView(ctx, view)
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
	if freq := strings.TrimSpace(view.GetFreq()); freq != "" && freq != dataset.GetFreq() {
		return fmt.Errorf("view freq %q differs from dataset %s freq %q; a View takes its Dataset's freq", freq, dataset.GetDatasetId(), dataset.GetFreq())
	}
	view.Freq = dataset.GetFreq()
	view.DatasetId = datasetID
	view.GrainKeys = defaultViewGrainKeys(dataset.GetDataKind())
	view.Engine = defaultViewEngine(dataset.GetDataKind())
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

func (s *Service) ListViewColumns(ctx context.Context, req *pb.ListViewColumnsReq) (*pb.ListViewColumnsRsp, error) {
	items, page, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), req.GetPage())
	if err != nil {
		return &pb.ListViewColumnsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListViewColumnsRsp{RetInfo: retinfo.Success("success"), Columns: items, PageResult: page}, nil
}
