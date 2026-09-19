package primarystore

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type datasetAdminDataNodeClient interface {
	DeleteDatasetRows(context.Context, *pb.DeleteDatasetRowsReq) (*pb.DeleteDatasetRowsRsp, error)
}

// DeleteDatasetRows removes the physical rows before Metadata deletes the
// Dataset object. Only Collector-owned result Datasets may use this destructive
// path. The caller-side task check is still required, but the Storage boundary
// must also reject a valid Collector credential pointed at an unrelated Dataset.
func (s *Service) DeleteDatasetRows(ctx context.Context, req *pb.PrimaryDeleteDatasetRowsReq) (*pb.PrimaryDeleteDatasetRowsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	ctx = s.requestContext(ctx)
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("metadata snapshot is unavailable for dataset deletion"))}, nil
	}
	dataset, found := snapshot.GetDataset(req.GetSpaceId(), req.GetDatasetId())
	if !found || dataset == nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, errors.New("dataset not found"))}, nil
	}
	attrs := dataset.GetAttributes()
	if strings.TrimSpace(attrs["owner_module"]) != "collector" || strings.TrimSpace(attrs["collector_task_id"]) == "" {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("dataset is not a Collector task result"))}, nil
	}
	if err := s.validateMarkerCaller(req.GetAuthInfo(), req.GetSpaceId(), req.GetDatasetId(), "collector"); err != nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: markerError(err)}, nil
	}
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: markerError(err)}, nil
	}
	adminNode, ok := node.(datasetAdminDataNodeClient)
	if !ok {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode dataset admin runtime is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	response, err := adminNode.DeleteDatasetRows(ctx, &pb.DeleteDatasetRowsReq{AuthInfo: auth, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId()})
	if err != nil {
		return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.PrimaryDeleteDatasetRowsRsp{RetInfo: response.GetRetInfo(), DeletedRanges: response.GetDeletedRanges()}, nil
}
