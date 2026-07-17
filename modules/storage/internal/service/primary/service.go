package primary

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Options 保存 PrimaryStore 服务创建时的依赖与路径配置。
type Options struct {
	Root       string
	PebblePath string
	Pebble     device.FactStore
	Publisher  EnvelopePublisher
	Outbox     OutboxConfig
}

type EnvelopePublisher interface {
	PublishEnvelope(context.Context, []byte) error
}

// Service 实现主存分片上的事实行读写接口。
type Service struct {
	client *LocalClient
	relay  *OutboxRelay
}

var _ pb.PrimaryStoreService = (*Service)(nil)

func NewService(opts Options) *Service {
	svc := &Service{client: NewLocalClient(LocalClientOptions{Root: opts.Root, PebblePath: opts.PebblePath, Pebble: opts.Pebble, Outbox: opts.Outbox})}
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

func (s *Service) WritePrimaryRows(ctx context.Context, req *pb.WritePrimaryRowsReq) (*pb.WritePrimaryRowsRsp, error) {
	if req == nil {
		return &pb.WritePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	var err error
	if len(req.GetOutboxMessage()) > 0 {
		if err := validateOutboxMessage(req); err != nil {
			return &pb.WritePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		err = s.client.WriteRowsWithMessage(ctx, req.GetTarget(), req.GetRows(), req.GetOutboxMessage())
	} else {
		err = s.client.WriteRows(ctx, req.GetTarget(), req.GetRows())
	}
	if err != nil {
		return &pb.WritePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.WritePrimaryRowsRsp{RetInfo: response.Success("success")}, nil
}

func validateOutboxMessage(req *pb.WritePrimaryRowsReq) error {
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(req.GetOutboxMessage(), msg); err != nil {
		return fmt.Errorf("decode outbox message: %w", err)
	}
	if err := jetstream.ValidateMessage(msg, 16<<20); err != nil {
		return err
	}
	if req.GetTarget() == nil || strings.TrimSpace(req.GetTarget().GetNodeId()) == "" {
		return errors.New("target node_id is required for outbox writes")
	}
	if msg.GetProducer().GetInstanceId() != req.GetTarget().GetNodeId() {
		return fmt.Errorf("outbox producer instance_id %q does not match target node_id %q", msg.GetProducer().GetInstanceId(), req.GetTarget().GetNodeId())
	}
	if len(msg.GetPayload()) == 0 {
		return errors.New("outbox payload is required")
	}
	return nil
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

func (s *Service) DeletePrimaryRows(ctx context.Context, req *pb.DeletePrimaryRowsReq) (*pb.DeletePrimaryRowsRsp, error) {
	if req == nil || req.GetTarget() == nil || len(req.GetKeys()) == 0 {
		return &pb.DeletePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("target and keys are required"))}, nil
	}
	if err := s.client.DeleteRows(ctx, req.GetTarget(), req.GetKeys()); err != nil {
		return &pb.DeletePrimaryRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.DeletePrimaryRowsRsp{RetInfo: response.Success("success"), Deleted: uint32(len(req.GetKeys()))}, nil
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
