package marketfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
)

const timerClaimTimeout = 3 * time.Second

type TimerRuntimeClient interface {
	ClaimTimerBatch(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error)
}

type timerRuntimeRPCClient struct {
	proxy collectorpb.MarketFetchRuntimeClientProxy
}

func newTimerRuntimeRPCClient(target, nodeID string) TimerRuntimeClient {
	options := gatewayauth.NewTRPCClientOptions(target, nodeID, gatewayauth.CredentialsFromEnv())
	return &timerRuntimeRPCClient{proxy: collectorpb.NewMarketFetchRuntimeClientProxy(options...)}
}

func (c *timerRuntimeRPCClient) ClaimTimerBatch(ctx context.Context, request *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, fmt.Errorf("collector runtime client is not configured")
	}
	return c.proxy.ClaimTimerBatch(ctx, request, client.WithTimeout(timerClaimTimeout))
}

func claimTimerRequest(ctx context.Context, client TimerRuntimeClient, invocation TimerInvocation) (Request, bool, error) {
	if client == nil || invocation.Claim == nil {
		return Request{}, false, fmt.Errorf("collector runtime Claim client is not configured")
	}
	claimCtx, cancel := context.WithTimeout(ctx, timerClaimTimeout)
	defer cancel()
	response, err := client.ClaimTimerBatch(claimCtx, invocation.Claim)
	if err != nil {
		return Request{}, false, fmt.Errorf("claim timer batch: %w", err)
	}
	if response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != collectorpb.ErrorCode_SUCCESS {
		message := "empty or unsuccessful response"
		if response != nil && response.GetRetInfo() != nil && strings.TrimSpace(response.GetRetInfo().GetMsg()) != "" {
			message = response.GetRetInfo().GetMsg()
		}
		return Request{}, false, fmt.Errorf("claim timer batch failed: %s", message)
	}
	if !response.GetClaimed() {
		if len(response.GetRequestJson()) != 0 {
			return Request{}, false, fmt.Errorf("no-work Claim unexpectedly included a request")
		}
		return Request{}, false, nil
	}
	request, err := decodeClaimedTimerRequest(response.GetRequestJson())
	if err != nil {
		return Request{}, false, fmt.Errorf("decode claimed timer request: %w", err)
	}
	claim := invocation.Claim
	if request.SpaceID != claim.GetSpaceId() || request.FunctionName != claim.GetFunctionName() ||
		request.GroupID != int(claim.GetGroupId()) || request.GroupCount != int(claim.GetGroupCount()) ||
		request.BindingHash != claim.GetBindingHash() ||
		(request.RequestID != "" && request.RequestID != claim.GetRequestId()) {
		return Request{}, false, fmt.Errorf("claimed timer request identity does not match runtime Claim")
	}
	if !request.RequirePeriodCommit {
		return Request{}, false, fmt.Errorf("claimed timer request does not require period commit")
	}
	request.RequestID = claim.GetRequestId()
	request.DNSRoutes = mergeDNSRoutes(invocation.DNSRoutes, request.DNSRoutes)
	return request, true, nil
}

func decodeClaimedTimerRequest(raw []byte) (Request, error) {
	if len(raw) == 0 || len(raw) > maxTimerBatchResponseBytes {
		return Request{}, fmt.Errorf("persisted timer request is empty or exceeds %d bytes", maxTimerBatchResponseBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return Request{}, fmt.Errorf("persisted timer request has trailing JSON")
		}
		return Request{}, err
	}
	if err := request.validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}
