package primarystore

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// DeleteDatasetRows 删除一个数据集的全部行，元数据保留。删除整个数据集用 Metadata.DeleteDataset，它会一并删行。
// 谁能调用由网关 ACL 决定，这里只校验服务签名。
func (s *Service) DeleteDatasetRows(ctx context.Context, req *pb.PrimaryDeleteDatasetRowsReq) (*pb.PrimaryDeleteDatasetRowsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("metadata snapshot is unavailable for dataset deletion"))}, nil
	}
	if dataset, found := snapshot.GetDataset(req.GetSpaceId(), req.GetDatasetId()); !found || dataset == nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, errors.New("dataset not found"))}, nil
	}
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: markerError(err)}, nil
	}
	deleted, err := s.store.DeleteDatasetRows(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: storeError(err)}, nil
	}
	return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Success("success"), DeletedRanges: deleted}, nil
}

// RestoreDatasetRows 已无作用：删除墓碑已去掉，重建同名数据集后直接写入即可。只为调用方改造前还能编译而保留。
func (s *Service) RestoreDatasetRows(_ context.Context, req *pb.PrimaryRestoreDatasetRowsReq) (*pb.PrimaryRestoreDatasetRowsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.PrimaryRestoreDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.PrimaryRestoreDatasetRowsRsp{RetInfo: markerError(err)}, nil
	}
	return &pb.PrimaryRestoreDatasetRowsRsp{RetInfo: retinfo.Success("success")}, nil
}
