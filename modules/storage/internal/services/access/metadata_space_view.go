package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// 本文件聚合 Space 与 View（含 ViewColumn）相关的元数据 CRUD 入口。

func (s *Service) CreateSpace(ctx context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || (space.GetSpaceId() == "" && space.GetName() == "") {
		return &pb.CreateSpaceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id or name is required"))}, nil
	}
	if space.SpaceId == "" {
		space.SpaceId = defaultID(space.GetName(), "space")
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	created, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.CreateSpaceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateSpaceRsp{RetInfo: response.Success("success"), Space: created}, nil
}

func (s *Service) UpdateSpace(ctx context.Context, req *pb.UpdateSpaceReq) (*pb.UpdateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || space.GetSpaceId() == "" {
		return &pb.UpdateSpaceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	updated, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateSpaceRsp{RetInfo: response.Success("success"), Space: updated}, nil
}

func (s *Service) GetSpace(ctx context.Context, req *pb.GetSpaceReq) (*pb.GetSpaceRsp, error) {
	space, err := s.metadata.GetSpace(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.GetSpaceRsp{RetInfo: response.Error(pb.ErrorCode_SPACE_NOT_FOUND, err)}, nil
	}
	return &pb.GetSpaceRsp{RetInfo: response.Success("success"), Space: space}, nil
}

func (s *Service) ListSpaces(ctx context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	items, page, err := s.metadata.ListSpaces(ctx, req.GetOwner(), req.GetPage())
	if err != nil {
		return &pb.ListSpacesRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListSpacesRsp{RetInfo: response.Success("success"), Spaces: items, PageResult: page}, nil
}

func (s *Service) CreateView(ctx context.Context, req *pb.CreateViewReq) (*pb.CreateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || (view.GetViewId() == "" && view.GetName() == "") {
		return &pb.CreateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id or name are required"))}, nil
	}
	if view.ViewId == "" {
		view.ViewId = defaultID(view.GetName(), "view")
	}
	if err := validateChineseDisplayName("view name", view.GetName()); err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewID(view.GetViewId()); err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertView(ctx, view)
	if err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateViewRsp{RetInfo: response.Success("success"), View: created}, nil
}

func (s *Service) UpdateView(ctx context.Context, req *pb.UpdateViewReq) (*pb.UpdateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return &pb.UpdateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	existing, existingErr := s.metadata.GetView(ctx, view.GetSpaceId(), view.GetViewId())
	if existingErr == nil && isViewBuildStateOnlyUpdate(existing, view) {
		updated, err := s.metadata.UpsertView(ctx, view)
		if err != nil {
			return &pb.UpdateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
		}
		return &pb.UpdateViewRsp{RetInfo: response.Success("success"), View: updated}, nil
	}
	if view.Name == "" {
		view.Name = view.GetViewId()
	}
	if err := validateChineseDisplayName("view name", view.GetName()); err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewID(view.GetViewId()); err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	updated, err := s.metadata.UpsertView(ctx, view)
	if err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateViewRsp{RetInfo: response.Success("success"), View: updated}, nil
}

func isViewBuildStateOnlyUpdate(existing *pb.View, next *pb.View) bool {
	if existing == nil || next == nil {
		return false
	}
	left := proto.Clone(existing).(*pb.View)
	right := proto.Clone(next).(*pb.View)
	clearViewBuildState(left)
	clearViewBuildState(right)
	return proto.Equal(left, right)
}

func clearViewBuildState(view *pb.View) {
	view.ActiveResult = ""
	view.BuildStatus = ""
	view.ActiveViewVersion = 0
	view.BuildingViewVersion = 0
	view.BuildingResult = ""
	view.BuildError = ""
	view.BuildStartedAt = ""
	view.BuildFinishedAt = ""
}

func (s *Service) normalizeAndValidateViewDatasets(ctx context.Context, view *pb.View) error {
	if view == nil {
		return errors.New("view is required")
	}
	spaceID := strings.TrimSpace(view.GetSpaceId())
	primaryDatasetID := strings.TrimSpace(view.GetPrimaryDatasetId())
	if spaceID == "" || primaryDatasetID == "" {
		return errors.New("space_id and primary_dataset_id are required")
	}
	datasetIDs := normalizeViewDatasetIDs(primaryDatasetID, view.GetDatasetIds())
	var primary *pb.Dataset
	datasets := make([]*pb.Dataset, 0, len(datasetIDs))
	for idx, datasetID := range datasetIDs {
		dataset, err := s.metadata.GetDataset(ctx, spaceID, datasetID)
		if err != nil {
			return fmt.Errorf("view dataset %s not found: %w", datasetID, err)
		}
		datasets = append(datasets, dataset)
		if idx == 0 {
			primary = dataset
		}
	}
	if primary == nil {
		return errors.New("view datasets are required")
	}
	if primary.GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES {
		freq, normalizedFilterJSON, err := normalizeTimeSeriesViewFilterJSON(view.GetFilterJson())
		if err != nil {
			return err
		}
		for _, dataset := range datasets {
			if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
				continue
			}
			if !datasetSupportsFreq(dataset, freq) {
				return fmt.Errorf("view dataset %s does not support freq %q", dataset.GetDatasetId(), freq)
			}
		}
		view.FilterJson = normalizedFilterJSON
	}
	view.PrimaryDatasetId = primaryDatasetID
	view.DatasetIds = datasetIDs
	view.GrainKeys = defaultViewGrainKeys(primary.GetDataKind())
	view.Engine = defaultViewEngine(primary.GetDataKind())
	return nil
}

func normalizeTimeSeriesViewFilterJSON(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("time series view filter_json.freq is required")
	}
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", "", fmt.Errorf("invalid time series view filter_json: %w", err)
	}
	var freq string
	if rawFreq, ok := fields["freq"]; ok {
		if err := json.Unmarshal(rawFreq, &freq); err != nil {
			return "", "", errors.New("time series view filter_json.freq must be a string")
		}
	}
	freq = strings.TrimSpace(freq)
	if freq == "" {
		return "", "", errors.New("time series view filter_json.freq is required")
	}
	encodedFreq, err := json.Marshal(freq)
	if err != nil {
		return "", "", err
	}
	fields["freq"] = encodedFreq
	normalized, err := json.Marshal(fields)
	if err != nil {
		return "", "", err
	}
	return freq, string(normalized), nil
}

func (s *Service) GetView(ctx context.Context, req *pb.GetViewReq) (*pb.GetViewRsp, error) {
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.GetViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	return &pb.GetViewRsp{RetInfo: response.Success("success"), View: view}, nil
}

func (s *Service) ListViews(ctx context.Context, req *pb.ListViewsReq) (*pb.ListViewsRsp, error) {
	items, page, err := s.metadata.ListViews(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetStatus(), req.GetPage())
	if err != nil {
		return &pb.ListViewsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListViewsRsp{RetInfo: response.Success("success"), Views: items, PageResult: page}, nil
}

func (s *Service) UpsertViewColumn(ctx context.Context, req *pb.UpsertViewColumnReq) (*pb.UpsertViewColumnRsp, error) {
	column := req.GetColumn()
	if column == nil || column.GetSpaceId() == "" || column.GetViewId() == "" || column.GetColumnName() == "" {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, view_id and column_name are required"))}, nil
	}
	if err := validateColumnDisplayName("view column display_name", column.GetAttributes()); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if err := validateViewColumnName(column); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertViewColumn(ctx, column)
	if err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertViewColumnRsp{RetInfo: response.Success("success"), Column: created}, nil
}

func (s *Service) ListViewColumns(ctx context.Context, req *pb.ListViewColumnsReq) (*pb.ListViewColumnsRsp, error) {
	items, page, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), req.GetPage())
	if err != nil {
		return &pb.ListViewColumnsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListViewColumnsRsp{RetInfo: response.Success("success"), Columns: items, PageResult: page}, nil
}
