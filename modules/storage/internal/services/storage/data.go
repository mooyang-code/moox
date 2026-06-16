package storage

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (s *Service) WriteRows(ctx context.Context, req *pb.WriteRowsReq) (*pb.WriteRowsRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	if err := s.store.WriteRows(ctx, req.GetRows(), mode); err != nil {
		return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.WriteRowsRsp{RetInfo: quantstore.Success("success")}, nil
}

func (s *Service) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	rows, page, err := s.store.ReadRows(
		ctx,
		req.GetScope(),
		req.GetReadMode(),
		req.GetTimeRange(),
		req.GetSnapshotTime(),
		req.GetRowIds(),
		req.GetColumnNames(),
		req.GetPage(),
	)
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ReadRowsRsp{RetInfo: quantstore.Success("success"), Rows: rows, PageResult: page}, nil
}
