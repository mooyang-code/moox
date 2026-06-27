package primary

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// Options 保存 PrimaryStore 服务创建时的依赖与路径配置。
type Options struct {
	Root       string
	PebblePath string
	Pebble     device.FactStore
}

// Service 实现主存分片上的事实行读写接口。
type Service struct {
	client *LocalClient
}

var _ pb.PrimaryStoreService = (*Service)(nil)

func NewService(opts Options) *Service {
	return &Service{client: NewLocalClient(LocalClientOptions(opts))}
}

func (s *Service) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *Service) WritePrimaryRows(ctx context.Context, req *pb.WritePrimaryRowsReq) (*pb.WritePrimaryRowsRsp, error) {
	if err := s.client.WriteRows(ctx, req.GetTarget(), req.GetRows()); err != nil {
		return &pb.WritePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.WritePrimaryRowsRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) ReadPrimaryRows(ctx context.Context, req *pb.ReadPrimaryRowsReq) (*pb.ReadPrimaryRowsRsp, error) {
	readReq := normalizeReadPrimaryRowsReq(req)
	rows, page, err := s.client.ReadRows(ctx, readReq.GetTarget(), readReq)
	if err != nil {
		return &pb.ReadPrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ReadPrimaryRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) ScanPrimaryRows(ctx context.Context, req *pb.ScanPrimaryRowsReq) (*pb.ScanPrimaryRowsRsp, error) {
	scanReq := normalizeScanPrimaryRowsReq(req)
	rows, page, err := s.client.ScanRows(ctx, scanReq.GetTarget(), scanReq)
	if err != nil {
		return &pb.ScanPrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ScanPrimaryRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func normalizeReadPrimaryRowsReq(req *pb.ReadPrimaryRowsReq) *pb.ReadPrimaryRowsReq {
	if req == nil {
		return &pb.ReadPrimaryRowsReq{}
	}
	cloned := proto.Clone(req).(*pb.ReadPrimaryRowsReq)
	target := cloned.GetTarget()
	for _, key := range cloned.GetKeys() {
		if key.GetSpaceId() == "" {
			key.SpaceId = target.GetSpaceId()
		}
		if key.GetDatasetId() == "" {
			key.DatasetId = target.GetDatasetId()
		}
	}
	return cloned
}

func normalizeScanPrimaryRowsReq(req *pb.ScanPrimaryRowsReq) *pb.ScanPrimaryRowsReq {
	if req == nil {
		return &pb.ScanPrimaryRowsReq{}
	}
	return proto.Clone(req).(*pb.ScanPrimaryRowsReq)
}
