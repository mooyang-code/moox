package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

func TestMarketFetchRuntimeClaimTimerBatchMapsDurableClaim(t *testing.T) {
	deadline := time.Date(2026, 9, 30, 12, 1, 0, 0, time.UTC)
	claimer := &runtimeTimerClaimerStub{response: marketfetch.TimerBatchClaimResponse{
		Claimed: true, RequestJSON: []byte(`{"batch_id":"batch-1"}`), PeriodDeadlineAt: deadline, BatchID: "batch-1",
	}}
	runtime := NewMarketFetchRuntime(claimer)
	request := &pb.ClaimTimerBatchReq{
		SpaceId: "crypto", FunctionName: "timer-function", RequestId: "request-1", GroupId: 2,
		GroupCount: 4, BindingHash: "binding-hash", TickTime: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).Unix(),
	}

	response, err := runtime.ClaimTimerBatch(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	require.True(t, response.GetClaimed())
	require.Equal(t, []byte(`{"batch_id":"batch-1"}`), response.GetRequestJson())
	require.Equal(t, deadline.Unix(), response.GetPeriodDeadlineAt())
	require.Equal(t, store.TimerPeriodBatchClaimInput{
		SpaceID: "crypto", FunctionName: "timer-function", RequestID: "request-1", GroupID: 2,
		GroupCount: 4, BindingHash: "binding-hash", TickTime: time.Unix(request.GetTickTime(), 0).UTC(),
	}, claimer.input)
}

func TestMarketFetchRuntimeClaimTimerBatchValidatesRequestAndPropagatesErrors(t *testing.T) {
	runtime := NewMarketFetchRuntime(&runtimeTimerClaimerStub{})
	response, err := runtime.ClaimTimerBatch(context.Background(), &pb.ClaimTimerBatchReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, response.GetRetInfo().GetCode())
	require.False(t, response.GetClaimed())

	claimer := &runtimeTimerClaimerStub{err: errors.New("sqlite unavailable")}
	runtime = NewMarketFetchRuntime(claimer)
	response, err = runtime.ClaimTimerBatch(context.Background(), &pb.ClaimTimerBatchReq{
		SpaceId: "crypto", FunctionName: "timer-function", RequestId: "request-1", GroupCount: 1,
		BindingHash: "binding-hash", TickTime: time.Now().Unix(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	require.Contains(t, response.GetRetInfo().GetMsg(), "sqlite unavailable")
}

func TestMarketFetchRuntimeClaimTimerBatchReturnsNoWorkWithoutPayload(t *testing.T) {
	runtime := NewMarketFetchRuntime(&runtimeTimerClaimerStub{})
	response, err := runtime.ClaimTimerBatch(context.Background(), &pb.ClaimTimerBatchReq{
		SpaceId: "crypto", FunctionName: "timer-function", RequestId: "request-1", GroupCount: 1,
		BindingHash: "binding-hash", TickTime: time.Now().Unix(),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	require.False(t, response.GetClaimed())
	require.Empty(t, response.GetRequestJson())
	require.Zero(t, response.GetPeriodDeadlineAt())
}

type runtimeTimerClaimerStub struct {
	input    store.TimerPeriodBatchClaimInput
	response marketfetch.TimerBatchClaimResponse
	err      error
}

func (s *runtimeTimerClaimerStub) Claim(_ context.Context, input store.TimerPeriodBatchClaimInput) (marketfetch.TimerBatchClaimResponse, error) {
	s.input = input
	return s.response, s.err
}
