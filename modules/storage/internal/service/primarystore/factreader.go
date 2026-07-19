package primarystore

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	primary "github.com/mooyang-code/moox/modules/storage/internal/service/datashard"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// primaryFactReader 通过 PrimaryStore 客户端回读主存事实行。
type primaryFactReader struct {
	service *Service
}

func (s *Service) primaryFactReader() *primaryFactReader {
	return &primaryFactReader{service: s}
}

// FactReader exposes the local cursor reader without making the public Read RPC
// carry internal dataset-scan semantics.
func (s *Service) FactReader() *primaryFactReader {
	return s.primaryFactReader()
}

func (s *Service) scanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	req := &pb.ReadTimeSeriesRowsReq{
		Keys:        []*pb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: datasetID}},
		TimeRange:   timeRange,
		ColumnNames: columnNames,
		Page:        page,
	}
	if err := validateTimeRange(req.GetTimeRange()); err != nil {
		return nil, nil, err
	}
	if isTimeSeriesDatasetScan(req) {
		return s.scanTimeSeriesDatasetPageRows(ctx, req)
	}
	rsp, err := s.ReadTimeSeriesRows(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, nil, errText(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (s *Service) scanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	req := &pb.ReadRecordRowsReq{
		Keys:         []*pb.RecordKey{{SpaceId: spaceID, DatasetId: datasetID}},
		VersionRange: versionRange,
		ColumnNames:  columnNames,
		Page:         page,
	}
	if isRecordDatasetScan(req) {
		return s.scanRecordDatasetPageRows(ctx, req)
	}
	rsp, err := s.ReadRecordRows(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, nil, errText(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

// ScanTimeSeriesRows is the internal cursor RPC used by independently deployed
// ViewBuilder processes. Each request reads one bounded PrimaryStore page.
func (s *Service) ScanTimeSeriesRows(ctx context.Context, req *pb.ScanTimeSeriesRowsReq) (*pb.ScanTimeSeriesRowsRsp, error) {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ScanTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	rows, page, err := s.scanTimeSeriesRows(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetTimeRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return &pb.ScanTimeSeriesRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows, PageResult: page}, nil
}

// ScanRecordRows is the record counterpart of ScanTimeSeriesRows.
func (s *Service) ScanRecordRows(ctx context.Context, req *pb.ScanRecordRowsReq) (*pb.ScanRecordRowsRsp, error) {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ScanRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	rows, page, err := s.scanRecordRows(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetVersionRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return &pb.ScanRecordRowsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) GetShardHeads(ctx context.Context, req *pb.GetShardHeadsReq) (*pb.GetShardHeadsRsp, error) {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	reader, ok := s.primary.(primary.HeadReader)
	if !ok {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errText("primary store does not expose shard heads"))}, nil
	}
	targets, err := s.router.ResolveDatasetTargets(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
	}
	heads := make([]*pb.ShardHead, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil || target.GetShardId() == "" {
			continue
		}
		if _, exists := seen[target.GetShardId()]; exists {
			continue
		}
		sequence, err := reader.HeadSequence(ctx, target)
		if err != nil {
			return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(primaryErrorCode(err), err)}, nil
		}
		seen[target.GetShardId()] = struct{}{}
		heads = append(heads, &pb.ShardHead{ShardId: target.GetShardId(), Sequence: sequence})
	}
	return &pb.GetShardHeadsRsp{RetInfo: retinfo.Success("success"), Heads: heads}, nil
}

func (r *primaryFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.service.ReadTimeSeriesRows(ctx, req)
}

func (r *primaryFactReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return r.service.scanTimeSeriesRows(ctx, spaceID, datasetID, timeRange, columnNames, page)
}

func (r *primaryFactReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return r.service.ReadRecordRows(ctx, req)
}

func (r *primaryFactReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return r.service.scanRecordRows(ctx, spaceID, datasetID, versionRange, columnNames, page)
}

func (r *primaryFactReader) ShardHeads(ctx context.Context, spaceID string, datasetID string) (map[string]uint64, error) {
	rsp, err := r.service.GetShardHeads(ctx, &pb.GetShardHeadsReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errText(rsp.GetRetInfo().GetMsg())
	}
	heads := make(map[string]uint64, len(rsp.GetHeads()))
	for _, head := range rsp.GetHeads() {
		if head != nil && head.GetShardId() != "" {
			heads[head.GetShardId()] = head.GetSequence()
		}
	}
	return heads, nil
}

func (r *primaryFactReader) ShardHeadsForDatasets(ctx context.Context, spaceID string, datasetIDs []string) (map[string]uint64, error) {
	merged := make(map[string]uint64)
	seen := false
	for _, datasetID := range datasetIDs {
		heads, err := r.ShardHeads(ctx, spaceID, datasetID)
		if err != nil {
			return nil, err
		}
		for shardID, sequence := range heads {
			seen = true
			if sequence > merged[shardID] {
				merged[shardID] = sequence
			}
		}
	}
	if !seen {
		return nil, errors.New("primary shard freshness is unavailable")
	}
	return merged, nil
}

func (r *primaryFactReader) Ready(ctx context.Context) error {
	if r == nil || r.service == nil || !r.service.Ready() {
		return errors.New("primary store is not ready")
	}
	return nil
}
