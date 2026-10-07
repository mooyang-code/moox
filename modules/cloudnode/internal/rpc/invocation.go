package rpc

import (
	"context"
	"encoding/json"
	"fmt"

	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
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

func (s *Service) invokeNode(ctx context.Context, node *store.CloudNode, eventData any, invokeType string, qualifier string) (*pb.InvokeFunctionRsp, error) {
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, fmt.Errorf("cloud account not found: %s", node.CloudAccountID)
	}
	if account.Provider != "tencent" {
		return nil, fmt.Errorf("unsupported cloud provider: %s", account.Provider)
	}
	if s.credentialResolver == nil {
		return nil, fmt.Errorf("cloud credential resolver is not configured")
	}
	credential, err := s.credentialResolver.Resolve(ctx, *account)
	if err != nil {
		return nil, err
	}
	if s.scfClientFactory == nil {
		return nil, fmt.Errorf("scf client factory is not configured")
	}
	client := s.scfClientFactory(credential)
	if client == nil {
		return nil, fmt.Errorf("scf client is not configured")
	}
	resp, err := client.InvokeFunction(ctx, tencentscf.InvokeFunctionRequest{
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
