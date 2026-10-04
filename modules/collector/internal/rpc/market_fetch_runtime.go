package rpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

type timerBatchClaimer interface {
	Claim(context.Context, store.TimerPeriodBatchClaimInput) (marketfetch.TimerBatchClaimResponse, error)
}

// MarketFetchRuntime is the isolated runtime-only Claim surface. It accepts
// routing identity and a replay token, never work membership.
type MarketFetchRuntime struct {
	pb.UnimplementedMarketFetchRuntime
	claimer timerBatchClaimer
}

func NewMarketFetchRuntime(claimer timerBatchClaimer) *MarketFetchRuntime {
	return &MarketFetchRuntime{claimer: claimer}
}

func (s *MarketFetchRuntime) ClaimTimerBatch(ctx context.Context, req *pb.ClaimTimerBatchReq) (*pb.ClaimTimerBatchRsp, error) {
	if req == nil {
		return &pb.ClaimTimerBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	functionName := strings.TrimSpace(req.GetFunctionName())
	requestID := strings.TrimSpace(req.GetRequestId())
	bindingHash := strings.TrimSpace(req.GetBindingHash())
	if spaceID == "" || functionName == "" || requestID == "" || bindingHash == "" || req.GetGroupCount() == 0 || req.GetGroupId() >= req.GetGroupCount() || req.GetTickTime() <= 0 {
		return &pb.ClaimTimerBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id, function_name, request_id, binding_hash, valid group identity and tick_time are required")}, nil
	}
	if len(spaceID) > 128 || len(functionName) > 256 || len(requestID) > 256 || len(bindingHash) > 256 {
		return &pb.ClaimTimerBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "claim identity exceeds its maximum length")}, nil
	}
	if s == nil || s.claimer == nil {
		return &pb.ClaimTimerBatchRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "timer batch claimer is not configured")}, nil
	}
	claimed, err := s.claimer.Claim(ctx, store.TimerPeriodBatchClaimInput{
		SpaceID: spaceID, FunctionName: functionName, RequestID: requestID,
		GroupID: req.GetGroupId(), GroupCount: req.GetGroupCount(), BindingHash: bindingHash,
		TickTime: time.Unix(req.GetTickTime(), 0).UTC(),
	})
	if err != nil {
		return &pb.ClaimTimerBatchRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, fmt.Sprintf("claim timer batch: %v", err))}, nil
	}
	if !claimed.Claimed {
		return &pb.ClaimTimerBatchRsp{RetInfo: retOK()}, nil
	}
	if len(claimed.RequestJSON) == 0 {
		return &pb.ClaimTimerBatchRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "claimed timer batch has no persisted request_json")}, nil
	}
	return &pb.ClaimTimerBatchRsp{
		RetInfo: retOK(), Claimed: true, RequestJson: append([]byte(nil), claimed.RequestJSON...),
		PeriodDeadlineAt: claimed.PeriodDeadlineAt.UTC().Unix(),
	}, nil
}
