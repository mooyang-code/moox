package rpc

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

func (s *Service) AcquireProviderPermit(ctx context.Context, req *pb.AcquireProviderPermitReq) (*pb.AcquireProviderPermitRsp, error) {
	if s.marketControl == nil || strings.TrimSpace(req.GetProviderId()) == "" || strings.TrimSpace(req.GetQuotaLeaseId()) == "" || strings.TrimSpace(req.GetExecutionNonce()) == "" || req.GetRequestCost() <= 0 {
		return &pb.AcquireProviderPermitRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "provider, lease, execution nonce and positive cost are required")}, nil
	}
	windows := make([]repository.QuotaWindow, 0, len(req.GetWindows()))
	for _, window := range req.GetWindows() {
		if window.GetWindowSeconds() <= 0 || window.GetLimit() <= 0 {
			return &pb.AcquireProviderPermitRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "quota windows must be positive")}, nil
		}
		windows = append(windows, repository.QuotaWindow{WindowSeconds: window.GetWindowSeconds(), Limit: window.GetLimit()})
	}
	permit, err := s.marketControl.AcquirePermit(ctx, repository.PermitRequest{ProviderID: req.GetProviderId(), ScopeKey: req.GetScopeKey(), EndpointClass: req.GetEndpointClass(), Cost: req.GetRequestCost(), LeaseID: req.GetQuotaLeaseId(), LeaseEpoch: req.GetLeaseEpoch(), ExecutionNonce: req.GetExecutionNonce(), RequestIndex: int(req.GetRequestIndex()), Now: time.Now().UTC(), Windows: windows})
	if err != nil {
		return &pb.AcquireProviderPermitRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.AcquireProviderPermitRsp{RetInfo: retOK(), PermitId: permit.PermitID, LeaseEpoch: permit.LeaseEpoch, Allowed: permit.Allowed, DenialReason: permit.DenialReason, ExpiresAt: permit.ExpiresAt.Format(time.RFC3339Nano)}
	if !permit.NotBefore.IsZero() {
		rsp.NotBefore = permit.NotBefore.Format(time.RFC3339Nano)
	}
	return rsp, nil
}

func (s *Service) ValidateMarketLease(ctx context.Context, req *pb.ValidateMarketLeaseReq) (*pb.ValidateMarketLeaseRsp, error) {
	if s.marketControl == nil {
		return &pb.ValidateMarketLeaseRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "market control unavailable")}, nil
	}
	if err := s.marketControl.ValidateLease(ctx, req.GetLeaseId(), req.GetLeaseType(), req.GetLeaseEpoch(), time.Now().UTC()); err != nil {
		return &pb.ValidateMarketLeaseRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error()), Valid: false}, nil
	}
	return &pb.ValidateMarketLeaseRsp{RetInfo: retOK(), Valid: true}, nil
}
