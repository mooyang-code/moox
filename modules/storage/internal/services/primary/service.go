package primary

import (
	"context"
	"strings"

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

func (s *Service) ApplyPrimaryRecordMutations(ctx context.Context, req *pb.ApplyPrimaryRecordMutationsReq) (*pb.ApplyPrimaryRecordMutationsRsp, error) {
	event, err := s.client.ApplyRecordMutations(ctx, req.GetSourceTarget(), req.GetRequestId(), req.GetMutations())
	if err != nil {
		return &pb.ApplyPrimaryRecordMutationsRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	return &pb.ApplyPrimaryRecordMutationsRsp{RetInfo: response.Success("success"), Commit: event}, nil
}

func (s *Service) OpenRecordSnapshot(ctx context.Context, req *pb.OpenRecordSnapshotReq) (*pb.OpenRecordSnapshotRsp, error) {
	rsp, err := s.client.OpenRecordSnapshot(ctx, req)
	if err != nil {
		return &pb.OpenRecordSnapshotRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	rsp.RetInfo = response.Success("success")
	return rsp, nil
}

func (s *Service) ReadRecordSnapshot(ctx context.Context, req *pb.ReadRecordSnapshotReq) (*pb.ReadRecordSnapshotRsp, error) {
	rsp, err := s.client.ReadRecordSnapshot(ctx, req)
	if err != nil {
		return &pb.ReadRecordSnapshotRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	rsp.RetInfo = response.Success("success")
	return rsp, nil
}

func (s *Service) ScanRecordSnapshot(ctx context.Context, req *pb.ScanRecordSnapshotReq) (*pb.ScanRecordSnapshotRsp, error) {
	rsp, err := s.client.ScanRecordSnapshot(ctx, req)
	if err != nil {
		return &pb.ScanRecordSnapshotRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	rsp.RetInfo = response.Success("success")
	return rsp, nil
}

func (s *Service) RenewRecordSnapshot(ctx context.Context, req *pb.RenewRecordSnapshotReq) (*pb.RenewRecordSnapshotRsp, error) {
	if err := s.client.RenewRecordSnapshot(ctx, req); err != nil {
		return &pb.RenewRecordSnapshotRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	return &pb.RenewRecordSnapshotRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) CloseRecordSnapshot(ctx context.Context, req *pb.CloseRecordSnapshotReq) (*pb.CloseRecordSnapshotRsp, error) {
	if err := s.client.CloseRecordSnapshot(ctx, req); err != nil {
		return &pb.CloseRecordSnapshotRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	return &pb.CloseRecordSnapshotRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) GetRecordWatermark(ctx context.Context, req *pb.GetRecordWatermarkReq) (*pb.GetRecordWatermarkRsp, error) {
	source, seq, err := s.client.GetRecordWatermark(ctx, req.GetTarget())
	if err != nil {
		return &pb.GetRecordWatermarkRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	return &pb.GetRecordWatermarkRsp{RetInfo: response.Success("success"), SourceId: source, CommitSeq: seq}, nil
}

func (s *Service) ScanRecordJournal(ctx context.Context, req *pb.ScanRecordJournalReq) (*pb.ScanRecordJournalRsp, error) {
	rsp, err := s.client.ScanRecordJournal(ctx, req)
	if err != nil {
		return &pb.ScanRecordJournalRsp{RetInfo: response.Error(recordErrorCode(err), err)}, nil
	}
	rsp.RetInfo = response.Success("success")
	return rsp, nil
}

func recordErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "revision conflict"):
		return pb.ErrorCode_REVISION_CONFLICT
	case strings.Contains(message, "snapshot not found"), strings.Contains(message, "snapshot proxy not found"):
		return pb.ErrorCode_NOT_FOUND
	case strings.Contains(message, "cursor"):
		return pb.ErrorCode_INVALID_PARAM
	default:
		return pb.ErrorCode_INVALID_PARAM
	}
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
