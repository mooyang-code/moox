//go:build legacy_storage

package datashard

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Options 保存 PrimaryStore 服务创建时的依赖与路径配置。
type Options struct {
	Root       string
	PebblePath string
	ShardID    string
	Pebble     contracts.FactStore
	Publisher  MessagePublisher
	Outbox     OutboxConfig
}

type MessagePublisher interface {
	PublishMessage(context.Context, []byte) error
}

// Service 实现主存分片上的事实行读写接口。
type Service struct {
	client        *LocalClient
	relay         *OutboxRelay
	relayRequired bool
}

var _ pb.DataShardService = (*Service)(nil)

func NewService(opts Options) *Service {
	svc := &Service{client: NewLocalClient(LocalClientOptions{Root: opts.Root, PebblePath: opts.PebblePath, ShardID: opts.ShardID, Pebble: opts.Pebble, Outbox: opts.Outbox}), relayRequired: opts.Publisher != nil}
	if opts.Publisher != nil {
		if store, err := svc.client.factStore(); err == nil {
			svc.relay = NewOutboxRelay(store, opts.Publisher, opts.Outbox)
			svc.relay.Start(trpc.BackgroundContext())
		}
	}
	return svc
}

func (s *Service) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	if s.relay != nil {
		_ = s.relay.Close()
	}
	return s.client.Close()
}

func (s *Service) OutboxReady() bool {
	if s == nil {
		return false
	}
	if s.relay == nil {
		return !s.relayRequired
	}
	return s.relay.Ready()
}

func (s *Service) Ready() bool {
	if s == nil || s.client == nil || !s.client.Ready(trpc.BackgroundContext()) {
		return false
	}
	return s.OutboxReady()
}

func (s *Service) MergeRows(ctx context.Context, req *pb.MergeRowsReq) (*pb.MergeRowsRsp, error) {
	if req == nil {
		return &pb.MergeRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	err := s.client.WriteRows(ctx, req.GetTarget(), req.GetRows())
	if err != nil {
		return &pb.MergeRowsRsp{RetInfo: retinfo.Error(dataShardErrorCode(err), err)}, nil
	}
	return &pb.MergeRowsRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	readReq := normalizeReadRowsReq(req)
	rows, page, err := s.client.ReadRows(ctx, readReq.GetTarget(), readReq)
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: retinfo.Error(dataShardErrorCode(err), err)}, nil
	}
	return &pb.ReadRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) ScanRows(ctx context.Context, req *pb.ScanRowsReq) (*pb.ScanRowsRsp, error) {
	scanReq := normalizeScanRowsReq(req)
	rows, page, err := s.client.ScanRows(ctx, scanReq.GetTarget(), scanReq)
	if err != nil {
		return &pb.ScanRowsRsp{RetInfo: retinfo.Error(dataShardErrorCode(err), err)}, nil
	}
	return &pb.ScanRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows, PageResult: page}, nil
}

func (s *Service) DeleteRows(ctx context.Context, req *pb.DeleteRowsReq) (*pb.DeleteRowsRsp, error) {
	if req == nil || req.GetTarget() == nil || len(req.GetKeys()) == 0 {
		return &pb.DeleteRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("target and keys are required"))}, nil
	}
	if err := s.client.DeleteRows(ctx, req.GetTarget(), req.GetKeys()); err != nil {
		return &pb.DeleteRowsRsp{RetInfo: retinfo.Error(dataShardErrorCode(err), err)}, nil
	}
	return &pb.DeleteRowsRsp{RetInfo: retinfo.Success("success"), Deleted: uint32(len(req.GetKeys()))}, nil
}

func (s *Service) GetShardState(ctx context.Context, req *pb.GetShardStateReq) (*pb.GetShardStateRsp, error) {
	if req == nil || strings.TrimSpace(req.GetShardId()) == "" {
		return &pb.GetShardStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("shard_id is required"))}, nil
	}
	target := &pb.ShardTarget{ShardId: req.GetShardId(), Engine: "pebble"}
	sequence, err := s.client.HeadSequence(ctx, target)
	if err != nil {
		return &pb.GetShardStateRsp{RetInfo: retinfo.Error(dataShardErrorCode(err), err)}, nil
	}
	return &pb.GetShardStateRsp{RetInfo: retinfo.Success("success"), ShardId: req.GetShardId(), HeadSequence: sequence}, nil
}

func dataShardErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, " is required") || strings.Contains(text, "invalid ") || strings.Contains(text, "must be ") {
		return pb.ErrorCode_INVALID_PARAM
	}
	return pb.ErrorCode_INNER_ERR
}

func normalizeReadRowsReq(req *pb.ReadRowsReq) *pb.ReadRowsReq {
	if req == nil {
		return &pb.ReadRowsReq{}
	}
	cloned := proto.Clone(req).(*pb.ReadRowsReq)
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

func normalizeScanRowsReq(req *pb.ScanRowsReq) *pb.ScanRowsReq {
	if req == nil {
		return &pb.ScanRowsReq{}
	}
	return proto.Clone(req).(*pb.ScanRowsReq)
}
