package v2

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
)

func (s *Service) UpsertRecords(ctx context.Context, req *pb.UpsertRecordsReq) (*pb.UpsertRecordsRsp, error) {
	var records []*pb.Record
	for _, mutation := range req.GetMutations() {
		if mutation.GetRecord() != nil {
			records = append(records, mutation.GetRecord())
		}
	}
	affected, err := s.store.UpsertRecords(ctx, records, pb.WriteMode_WRITE_MODE_UPSERT)
	if err != nil {
		return &pb.UpsertRecordsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertRecordsRsp{RetInfo: quantstore.Success("success"), Affected: affected}, nil
}

func (s *Service) QueryRecords(ctx context.Context, req *pb.QueryRecordsReq) (*pb.QueryRecordsRsp, error) {
	records, page, err := s.store.QueryRecords(ctx, req.GetDataRef(), req.GetPage())
	if err != nil {
		return &pb.QueryRecordsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.QueryRecordsRsp{RetInfo: quantstore.Success("success"), Records: records, PageResult: page}, nil
}

func (s *Service) SetTimeSeries(ctx context.Context, req *pb.SetTimeSeriesReq) (*pb.SetTimeSeriesRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	affected, err := s.store.SetTimeSeries(ctx, req.GetPoints(), mode)
	if err != nil {
		return &pb.SetTimeSeriesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.SetTimeSeriesRsp{RetInfo: quantstore.Success("success"), Affected: affected}, nil
}

func (s *Service) ScanTimeSeries(ctx context.Context, req *pb.ScanTimeSeriesReq) (*pb.ScanTimeSeriesRsp, error) {
	points, page, err := s.store.ScanTimeSeries(ctx, req.GetDataRef(), req.GetTimeRange(), req.GetFieldNames(), req.GetPage())
	if err != nil {
		return &pb.ScanTimeSeriesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ScanTimeSeriesRsp{RetInfo: quantstore.Success("success"), Points: points, PageResult: page}, nil
}

func (s *Service) SetFactorValues(ctx context.Context, req *pb.SetFactorValuesReq) (*pb.SetFactorValuesRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	affected, err := s.store.SetFactorValues(ctx, req.GetPoints(), mode)
	if err != nil {
		return &pb.SetFactorValuesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.SetFactorValuesRsp{RetInfo: quantstore.Success("success"), Affected: affected}, nil
}

func (s *Service) ScanFactorValues(ctx context.Context, req *pb.ScanFactorValuesReq) (*pb.ScanFactorValuesRsp, error) {
	points, page, err := s.store.ScanFactorValues(ctx, req.GetDataRef(), req.GetFactorInstanceIds(), req.GetTimeRange(), req.GetPage())
	if err != nil {
		return &pb.ScanFactorValuesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ScanFactorValuesRsp{RetInfo: quantstore.Success("success"), Points: points, PageResult: page}, nil
}

func (s *Service) GetLatestSnapshot(ctx context.Context, req *pb.GetLatestSnapshotReq) (*pb.GetLatestSnapshotRsp, error) {
	if len(req.GetDataRefs()) == 0 {
		return &pb.GetLatestSnapshotRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("data_refs is required"))}, nil
	}
	rows, err := s.store.LatestSnapshot(ctx, req.GetDataRefs(), req.GetFieldNames(), req.GetSnapshotTime())
	if err != nil {
		return &pb.GetLatestSnapshotRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetLatestSnapshotRsp{RetInfo: quantstore.Success("success"), Rows: rows}, nil
}
