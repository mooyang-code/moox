package storage

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (s *Service) WriteDeviceRows(ctx context.Context, req *pb.WriteDeviceRowsReq) (*pb.WriteDeviceRowsRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	if err := s.store.WriteRows(ctx, req.GetRows(), mode); err != nil {
		return &pb.WriteDeviceRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.WriteDeviceRowsRsp{RetInfo: quantstore.Success("success")}, nil
}

func (s *Service) ReadDeviceRows(ctx context.Context, req *pb.ReadDeviceRowsReq) (*pb.ReadDeviceRowsRsp, error) {
	slice := req.GetSlice()
	if slice == nil {
		slice = &pb.DataSlice{}
	}
	if slice.DatasetId == "" {
		slice = &pb.DataSlice{
			DatasetId:  req.GetDevice().GetDatasetId(),
			SubjectId:  slice.GetSubjectId(),
			Freq:       slice.GetFreq(),
			Dimensions: slice.GetDimensions(),
		}
	}
	rows, page, err := s.store.ReadRows(
		ctx,
		slice,
		req.GetReadMode(),
		req.GetTimeRange(),
		req.GetSnapshotTime(),
		req.GetRowIds(),
		req.GetColumnNames(),
		req.GetPage(),
	)
	if err != nil {
		return &pb.ReadDeviceRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ReadDeviceRowsRsp{RetInfo: quantstore.Success("success"), Rows: rows, PageResult: page}, nil
}
