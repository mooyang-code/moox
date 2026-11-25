package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 本文件聚合 Space 相关的元数据 CRUD 入口。

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
	if err := validateSpaceTimezone(space); err != nil {
		return &pb.CreateSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	created, err := s.metadata.CreateSpace(ctx, space)
	if err != nil {
		return &pb.CreateSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
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
	if err := validateSpaceTimezone(space); err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	updated, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.UpdateSpaceRsp{RetInfo: retinfo.Success("success"), Space: updated}, nil
}

// validateSpaceTimezone 要求时区是 IANA 名称：管理台全站时间按它显示，写错了页面只能回退到浏览器本地时区。
func validateSpaceTimezone(space *pb.Space) error {
	space.Timezone = strings.TrimSpace(space.GetTimezone())
	space.Market = strings.TrimSpace(space.GetMarket())
	if space.Timezone == "" {
		return nil
	}
	if _, err := time.LoadLocation(space.Timezone); err != nil {
		return storeerr.Invalidf("空间时区 %q 不是有效的 IANA 时区名", space.Timezone)
	}
	return nil
}

func (s *Service) DeleteSpace(ctx context.Context, req *pb.DeleteSpaceReq) (*pb.DeleteSpaceRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	if s.rows == nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("dataset row store is not configured"))}, nil
	}
	// 和 DeleteDataset 一样服务端一次做完：先记下空间里的数据集，删元数据（级联删数据集与列）并发布缓存，
	// 再在写锁里逐个删行。数据集的查询索引由 storage-view 下一轮维护摘掉并删除。
	datasetIDs, err := s.spaceDatasetIDs(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := s.metadata.DeleteSpace(ctx, req.GetSpaceId()); err != nil {
		return &pb.DeleteSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
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
		return &pb.GetSpaceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.GetSpaceRsp{RetInfo: retinfo.Success("success"), Space: space}, nil
}

func (s *Service) ListSpaces(ctx context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	items, page, err := s.metadata.ListSpaces(ctx, req.GetOwner(), req.GetStatus(), req.GetPage())
	if err != nil {
		return &pb.ListSpacesRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.ListSpacesRsp{RetInfo: retinfo.Success("success"), Spaces: items, PageResult: page}, nil
}
