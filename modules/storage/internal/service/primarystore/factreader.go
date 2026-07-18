package primarystore

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
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
		return &pb.ScanTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	rows, page, err := s.scanTimeSeriesRows(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetTimeRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return &pb.ScanTimeSeriesRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

// ScanRecordRows is the record counterpart of ScanTimeSeriesRows.
func (s *Service) ScanRecordRows(ctx context.Context, req *pb.ScanRecordRowsReq) (*pb.ScanRecordRowsRsp, error) {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ScanRecordRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errText("space_id and dataset_id are required"))}, nil
	}
	rows, page, err := s.scanRecordRows(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetVersionRange(), req.GetColumnNames(), req.GetPage())
	if err != nil {
		return &pb.ScanRecordRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ScanRecordRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
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
