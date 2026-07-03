package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

func (s *Service) SubmitWorkItems(ctx context.Context, req *pb.SubmitWorkItemsReq) (*pb.SubmitWorkItemsRsp, error) {
	for i, item := range req.GetWorkItems() {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.GetSpaceId()) == "" {
			return &pb.SubmitWorkItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("work_items[%d].space_id is required", i))}, nil
		}
	}
	acks, err := s.workItemRepo.Submit(ctx, req.GetWorkItems())
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] submit work items failed: %v", err)
		return &pb.SubmitWorkItemsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.SubmitWorkItemsRsp{RetInfo: retOK(), WorkItems: acks, Submitted: int32(len(acks))}, nil
}

func (s *Service) PollWorkItems(ctx context.Context, req *pb.PollWorkItemsReq) (*pb.PollWorkItemsRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.PollWorkItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if req.GetNodeId() == "" {
		return &pb.PollWorkItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	workItems, err := s.workItemRepo.Poll(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] poll work items failed: %v", err)
		return &pb.PollWorkItemsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	leases := make([]*pb.CloudWorkItemLease, 0, len(workItems))
	for _, item := range workItems {
		leaseDeadline := ""
		if item.LeaseDeadline != nil {
			leaseDeadline = item.LeaseDeadline.UTC().Format(time.RFC3339)
		}
		leases = append(leases, &pb.CloudWorkItemLease{
			WorkItemId:    item.WorkItemID,
			OwnerService:  item.OwnerService,
			OwnerRef:      item.OwnerRef,
			WorkloadType:  item.WorkloadType,
			DeploymentId:  item.DeploymentID,
			Payload:       jsonStringToStruct(item.Payload),
			Priority:      int32(item.Priority),
			AttemptNo:     int32(item.AttemptNo),
			LeaseDeadline: leaseDeadline,
		})
	}
	return &pb.PollWorkItemsRsp{RetInfo: retOK(), WorkItems: leases, PollTime: Now().Format(time.RFC3339)}, nil
}

func (s *Service) ReportWorkItemStatus(ctx context.Context, req *pb.ReportWorkItemStatusReq) (*pb.ReportWorkItemStatusRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ReportWorkItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if req.GetWorkItemId() == "" {
		return &pb.ReportWorkItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "work_item_id is required")}, nil
	}
	if err := s.workItemRepo.Report(ctx, req); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] report work item status failed: %v", err)
		return &pb.ReportWorkItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.ReportWorkItemStatusRsp{RetInfo: retOK()}, nil
}

func jsonStringToStruct(raw string) *structpb.Struct {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &structpb.Struct{}
	}
	st := &structpb.Struct{}
	if err := protojson.Unmarshal([]byte(raw), st); err != nil {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			st, _ = structpb.NewStruct(obj)
			if st != nil {
				return st
			}
		}
		return &structpb.Struct{}
	}
	return st
}
