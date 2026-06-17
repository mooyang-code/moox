package primary

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Options struct {
	Root       string
	PebblePath string
	Pebble     device.FactStore
}

type Service struct {
	client *LocalClient
}

var _ pb.PrimaryStoreServiceService = (*Service)(nil)

func NewService(opts Options) *Service {
	return &Service{client: NewLocalClient(LocalClientOptions{Root: opts.Root, PebblePath: opts.PebblePath, Pebble: opts.Pebble})}
}

func (s *Service) WritePrimaryRows(ctx context.Context, req *pb.WritePrimaryRowsReq) (*pb.WritePrimaryRowsRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	if err := s.client.WriteRows(ctx, req.GetTarget(), req.GetRows(), mode); err != nil {
		return &pb.WritePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.WritePrimaryRowsRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) ReadPrimaryRows(ctx context.Context, req *pb.ReadPrimaryRowsReq) (*pb.ReadPrimaryRowsRsp, error) {
	readReq := normalizeReadPrimaryRowsReq(req)
	rows, page, err := s.client.ReadRows(ctx, readReq.GetTarget(), &pb.ReadRowsReq{
		AuthInfo:     readReq.GetAuthInfo(),
		Scope:        readReq.GetScope(),
		ReadMode:     readReq.GetReadMode(),
		TimeRange:    readReq.GetTimeRange(),
		SnapshotTime: readReq.GetSnapshotTime(),
		RowIds:       readReq.GetRowIds(),
		ColumnNames:  readReq.GetColumnNames(),
		Page:         readReq.GetPage(),
	})
	if err != nil {
		return &pb.ReadPrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ReadPrimaryRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func normalizeReadPrimaryRowsReq(req *pb.ReadPrimaryRowsReq) *pb.ReadPrimaryRowsReq {
	if req == nil {
		return &pb.ReadPrimaryRowsReq{}
	}
	scope := req.GetScope()
	if scope == nil {
		scope = &pb.DataScope{}
	}
	target := req.GetTarget()
	if scope.GetDatasetId() == "" {
		scope = &pb.DataScope{
			SpaceId:    target.GetSpaceId(),
			DatasetId:  target.GetDatasetId(),
			SubjectId:  scope.GetSubjectId(),
			Freq:       scope.GetFreq(),
			Dimensions: scope.GetDimensions(),
		}
	}
	if scope.GetSpaceId() == "" {
		scope.SpaceId = target.GetSpaceId()
	}
	cloned := *req
	cloned.Scope = scope
	return &cloned
}
