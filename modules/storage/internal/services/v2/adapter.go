package v2

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
)

func (s *Service) CreatePhysicalTable(_ context.Context, _ *pb.CreatePhysicalTableReq) (*pb.CreatePhysicalTableRsp, error) {
	return &pb.CreatePhysicalTableRsp{RetInfo: quantstore.Success("success")}, nil
}

func (s *Service) DropPhysicalTable(_ context.Context, _ *pb.DropPhysicalTableReq) (*pb.DropPhysicalTableRsp, error) {
	return &pb.DropPhysicalTableRsp{RetInfo: quantstore.Success("success")}, nil
}

func (s *Service) WriteRows(ctx context.Context, req *pb.WriteRowsReq) (*pb.WriteRowsRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	affected, err := s.store.UpsertRecords(ctx, req.GetRows(), mode)
	if err != nil {
		return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.WriteRowsRsp{RetInfo: quantstore.Success("success"), Affected: affected}, nil
}

func (s *Service) ScanRows(ctx context.Context, req *pb.ScanRowsReq) (*pb.ScanRowsRsp, error) {
	table := req.GetTable()
	ref := &pb.DataRef{DatasetId: table.GetDatasetId()}
	rows, page, err := s.store.QueryRecords(ctx, ref, req.GetPage())
	if err != nil {
		return &pb.ScanRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ScanRowsRsp{RetInfo: quantstore.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) ExplainRoute(_ context.Context, req *pb.ExplainRouteReq) (*pb.ExplainRouteRsp, error) {
	ref := req.GetDataRef()
	table := &pb.PhysicalTableRef{
		DeviceId:  "local-file",
		TableName: ref.GetWorkspaceId() + "_" + ref.GetDatasetId(),
		DatasetId: ref.GetDatasetId(),
	}
	return &pb.ExplainRouteRsp{RetInfo: quantstore.Success("success"), Tables: []*pb.PhysicalTableRef{table}}, nil
}
