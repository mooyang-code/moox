package rpc

import (
	"context"
	"strings"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

const supportedJobItemProtocolVersion = "cloudnode-jobitem-v1"

func (s *Service) SubmitJobItems(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	acks, err := s.jobItemRepo.Submit(ctx, req.GetItems())
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] submit job items failed: %v", err)
		return &pb.SubmitJobItemsRsp{RetInfo: retFromError(err)}, nil
	}
	var created, deduplicated, rejected int32
	for _, ack := range acks {
		switch ack.GetStatus() {
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED:
			created++
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED:
			deduplicated++
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED:
			rejected++
		}
	}
	return &pb.SubmitJobItemsRsp{
		RetInfo:      retOK(),
		Acks:         acks,
		Created:      created,
		Deduplicated: deduplicated,
		Rejected:     rejected,
	}, nil
}

func (s *Service) PollJobItems(ctx context.Context, req *pb.PollJobItemsReq) (*pb.PollJobItemsRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.PollJobItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return &pb.PollJobItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if len(compactStrings(req.GetSupportedJobTypes())) == 0 {
		return &pb.PollJobItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "supported_job_types is required")}, nil
	}
	if strings.TrimSpace(req.GetProtocolVersion()) != supportedJobItemProtocolVersion {
		return &pb.PollJobItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "unsupported protocol_version")}, nil
	}
	items, err := s.jobItemRepo.Poll(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] poll job items failed: %v", err)
		return &pb.PollJobItemsRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.PollJobItemsRsp{
		RetInfo:  retOK(),
		Items:    items,
		PollTime: timestamppb.New(Now()),
	}, nil
}

func (s *Service) ReportJobItemStatus(ctx context.Context, req *pb.ReportJobItemStatusReq) (*pb.ReportJobItemStatusRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ReportJobItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return &pb.ReportJobItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.ReportJobItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id is required")}, nil
	}
	if req.GetAttemptNo() <= 0 {
		return &pb.ReportJobItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "attempt_no is required")}, nil
	}
	if req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED &&
		req.GetErrorKind() == pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED {
		return &pb.ReportJobItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "error_kind is required when status is FAILED")}, nil
	}
	if err := s.jobItemRepo.Report(ctx, req); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] report job item status failed: %v", err)
		return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.ReportJobItemStatusRsp{RetInfo: retOK()}, nil
}

func (s *Service) CancelJobItem(ctx context.Context, req *pb.CancelJobItemReq) (*pb.CancelJobItemRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.CancelJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.CancelJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id is required")}, nil
	}
	if err := s.jobItemRepo.Cancel(ctx, req); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] cancel job item failed: %v", err)
		return &pb.CancelJobItemRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.CancelJobItemRsp{RetInfo: retOK()}, nil
}

func (s *Service) GetJobItem(ctx context.Context, req *pb.GetJobItemReq) (*pb.GetJobItemRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.GetJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id is required")}, nil
	}
	item, err := s.jobItemRepo.Get(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] get job item failed: %v", err)
		return &pb.GetJobItemRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.GetJobItemRsp{RetInfo: retOK(), Item: item}, nil
}

func (s *Service) ListJobItems(ctx context.Context, req *pb.ListJobItemsReq) (*pb.ListJobItemsRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ListJobItemsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	items, page, err := s.jobItemRepo.List(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] list job items failed: %v", err)
		return &pb.ListJobItemsRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.ListJobItemsRsp{RetInfo: retOK(), Items: items, Page: page}, nil
}

func (s *Service) ListJobItemAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) (*pb.ListJobItemAttemptsRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ListJobItemAttemptsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.ListJobItemAttemptsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id is required")}, nil
	}
	attempts, err := s.jobItemRepo.ListAttempts(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] list job item attempts failed: %v", err)
		return &pb.ListJobItemAttemptsRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.ListJobItemAttemptsRsp{RetInfo: retOK(), Attempts: attempts}, nil
}
