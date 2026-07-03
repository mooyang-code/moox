package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencent-scf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

func (s *Service) InvokeFunction(ctx context.Context, req *pb.InvokeFunctionReq) (*pb.InvokeFunctionRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	node, err := s.catalog.GetNode(ctx, spaceID, req.GetNodeId())
	if err != nil {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if node == nil {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "node not found")}, nil
	}
	event := map[string]any{}
	if req.GetEventData() != nil {
		event = req.GetEventData().AsMap()
	}
	rsp, err := s.invokeNode(ctx, node, event, scfInvokeTypeToString(req.GetScfInvokeType()), req.GetQualifier())
	if err != nil {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return rsp, nil
}

func (s *Service) InvokeSync(ctx context.Context, req *pb.InvokeSyncReq) (*pb.InvokeSyncRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.InvokeSyncRsp{
			RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required"),
			Status:  pb.InvocationStatus_INVOCATION_STATUS_FAILED,
		}, nil
	}
	start := time.Now()
	invocationID := "inv_" + fmt.Sprintf("%d", start.UnixNano())
	invokeCtx := ctx
	cancel := func() {}
	if req.GetTimeoutMs() > 0 {
		invokeCtx, cancel = context.WithTimeout(ctx, time.Duration(req.GetTimeoutMs())*time.Millisecond)
	}
	defer cancel()
	node, err := s.catalog.FindNodeForInvocation(invokeCtx, req.GetSpaceId(), req.GetDeploymentId(), req.GetWorkloadType())
	if err != nil {
		return &pb.InvokeSyncRsp{
			RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error()), InvocationId: invocationID,
			Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED,
		}, nil
	}
	if node == nil {
		return &pb.InvokeSyncRsp{
			RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "no cloud node matched invocation"), InvocationId: invocationID,
			Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED, FailedCount: int32(len(req.GetPayloads())),
		}, nil
	}
	results := s.invokePayloads(invokeCtx, node, req)
	success, failed, timeoutCount := countSyncResults(results)
	status := pb.InvocationStatus_INVOCATION_STATUS_SUCCESS
	if failed > 0 || timeoutCount > 0 {
		status = pb.InvocationStatus_INVOCATION_STATUS_PARTIAL_FAILED
	}
	if success == 0 && (failed > 0 || timeoutCount > 0) {
		status = pb.InvocationStatus_INVOCATION_STATUS_FAILED
	}
	duration := time.Since(start).Milliseconds()
	var details []repository.InvocationResult
	if req.GetRecordDetail() {
		details = make([]repository.InvocationResult, 0, len(results))
		for _, result := range results {
			details = append(details, repository.InvocationResult{
				SpaceID:      req.GetSpaceId(),
				InvocationID: invocationID,
				RequestID:    result.GetRequestId(),
				Status:       invocationResultStatusToDB(result.GetStatus()),
				Payload:      result.GetPayload(),
				ErrorMessage: result.GetErrorMessage(),
				DurationMS:   result.GetDurationMs(),
				CreateTime:   time.Now().UTC(),
			})
		}
	}
	if err := s.catalog.SaveInvocation(ctx, repository.Invocation{
		SpaceID:      req.GetSpaceId(),
		InvocationID: invocationID,
		OwnerService: req.GetOwnerService(),
		WorkloadType: req.GetWorkloadType(),
		DeploymentID: req.GetDeploymentId(),
		Status:       invocationStatusToDB(status),
		RequestCount: len(req.GetPayloads()),
		SuccessCount: int(success),
		FailedCount:  int(failed),
		TimeoutCount: int(timeoutCount),
		DurationMS:   duration,
		CreateTime:   time.Now().UTC(),
		ModifyTime:   time.Now().UTC(),
	}, details); err != nil {
		log.WarnContextf(ctx, "[CloudNode] save invocation failed: %v", err)
	}
	return &pb.InvokeSyncRsp{
		RetInfo:      retOK(),
		InvocationId: invocationID,
		Status:       status,
		SuccessCount: success,
		FailedCount:  failed,
		TimeoutCount: timeoutCount,
		DurationMs:   duration,
		Results:      results,
	}, nil
}

func (s *Service) invokePayloads(ctx context.Context, node *repository.CloudNode, req *pb.InvokeSyncReq) []*pb.InvokeSyncResult {
	payloads := req.GetPayloads()
	results := make([]*pb.InvokeSyncResult, len(payloads))
	parallelism := int(req.GetMaxParallelism())
	if parallelism <= 0 {
		parallelism = 1
	}
	if parallelism > 64 {
		parallelism = 64
	}
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, payload := range payloads {
		i, payload := i, payload
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = timeoutSyncResult(payload.GetRequestId(), ctx.Err())
				return
			}
			defer func() { <-sem }()
			start := time.Now()
			if err := ctx.Err(); err != nil {
				results[i] = timeoutSyncResult(payload.GetRequestId(), err)
				return
			}
			event := map[string]any{}
			if payload.GetPayload() != "" {
				_ = json.Unmarshal([]byte(payload.GetPayload()), &event)
			}
			rsp, err := s.invokeNode(ctx, node, event, "RequestResponse", "")
			duration := time.Since(start).Milliseconds()
			if err != nil {
				results[i] = &pb.InvokeSyncResult{RequestId: payload.GetRequestId(), Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED, ErrorMessage: err.Error(), DurationMs: duration}
				return
			}
			if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rsp.GetScf().GetCode() != 0 {
				results[i] = &pb.InvokeSyncResult{
					RequestId: payload.GetRequestId(), Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED,
					ErrorMessage: firstString(rsp.GetScf().GetMessage(), rsp.GetRetInfo().GetMsg()), DurationMs: duration,
				}
				return
			}
			raw, _ := json.Marshal(rsp.GetScf().GetResult().AsMap())
			results[i] = &pb.InvokeSyncResult{RequestId: payload.GetRequestId(), Status: pb.InvocationStatus_INVOCATION_STATUS_SUCCESS, Payload: string(raw), DurationMs: duration}
		}()
	}
	wg.Wait()
	return results
}

func (s *Service) invokeNode(ctx context.Context, node *repository.CloudNode, eventData any, invokeType string, qualifier string) (*pb.InvokeFunctionRsp, error) {
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, fmt.Errorf("cloud account not found: %s", node.CloudAccountID)
	}
	if strings.ToLower(account.Provider) != "tencent" && strings.ToLower(account.Provider) != "tencent-scf" {
		return nil, fmt.Errorf("unsupported cloud provider: %s", account.Provider)
	}
	resp, err := tencentscf.New(account.SecretID, account.SecretKey).InvokeFunction(ctx, tencentscf.InvokeFunctionRequest{
		Region:       node.Region,
		FunctionName: firstString(node.FunctionName, node.NodeID),
		Namespace:    firstString(node.Namespace, "default"),
		Qualifier:    qualifier,
		InvokeType:   invokeType,
		EventData:    eventData,
	})
	if err != nil {
		return nil, err
	}
	return &pb.InvokeFunctionRsp{
		RetInfo: retOK(),
		Scf: &pb.ScfInvokeResult{
			Code:         resp.Code,
			Message:      resp.Message,
			RequestId:    resp.RequestID,
			Result:       returnResultStruct(resp.ReturnResult),
			Duration:     resp.Duration,
			BillDuration: resp.BillDuration,
			MemoryUsage:  resp.MemoryUsage,
		},
	}, nil
}

func countSyncResults(results []*pb.InvokeSyncResult) (int32, int32, int32) {
	var success int32
	var failed int32
	var timeoutCount int32
	for _, result := range results {
		switch {
		case result.GetStatus() == pb.InvocationStatus_INVOCATION_STATUS_SUCCESS:
			success++
		case isTimeoutSyncResult(result):
			timeoutCount++
		default:
			failed++
		}
	}
	return success, failed, timeoutCount
}

func timeoutSyncResult(requestID string, err error) *pb.InvokeSyncResult {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timeout: " + msg
	}
	return &pb.InvokeSyncResult{
		RequestId:    requestID,
		Status:       pb.InvocationStatus_INVOCATION_STATUS_FAILED,
		ErrorMessage: msg,
	}
}

func isTimeoutSyncResult(result *pb.InvokeSyncResult) bool {
	return strings.HasPrefix(result.GetErrorMessage(), "timeout:")
}

func returnResultStruct(raw string) *structpb.Struct {
	if raw == "" {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		st, err := structpb.NewStruct(obj)
		if err == nil {
			return st
		}
	}
	st, _ := structpb.NewStruct(map[string]any{"raw": raw})
	return st
}

func scfInvokeTypeToString(t pb.ScfInvokeType) string {
	switch t {
	case pb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT:
		return "Event"
	default:
		return "RequestResponse"
	}
}

func invocationStatusToDB(status pb.InvocationStatus) string {
	switch status {
	case pb.InvocationStatus_INVOCATION_STATUS_SUCCESS:
		return "success"
	case pb.InvocationStatus_INVOCATION_STATUS_PARTIAL_FAILED:
		return "partial_failed"
	case pb.InvocationStatus_INVOCATION_STATUS_FAILED:
		return "failed"
	default:
		return "pending"
	}
}

func invocationResultStatusToDB(status pb.InvocationStatus) string {
	switch status {
	case pb.InvocationStatus_INVOCATION_STATUS_SUCCESS:
		return "success"
	default:
		return "failed"
	}
}
