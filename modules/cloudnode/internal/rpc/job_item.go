package rpc

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

const supportedJobItemProtocolVersion = "cloudnode-jobitem-v1"

func (s *Service) SubmitJobItems(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	if s.executionQueue != nil && s.projectionRepo != nil {
		return s.submitJobItemsWithJetStream(ctx, req)
	}
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
	if s.executionQueue != nil && s.projectionRepo != nil {
		items, err := s.pollJobItemsWithJetStream(ctx, req)
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
	if s.executionQueue != nil && s.projectionRepo != nil {
		if err := s.reportJobItemStatusWithJetStream(ctx, req); err != nil {
			log.ErrorContextf(ctx, "[CloudNode] report job item status failed: %v", err)
			return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(err)}, nil
		}
		return &pb.ReportJobItemStatusRsp{RetInfo: retOK()}, nil
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
	if s.projectionRepo != nil {
		if err := s.projectionRepo.MarkCanceled(ctx, req.GetSpaceId(), req.GetJobItemId(), "user canceled"); err != nil {
			log.ErrorContextf(ctx, "[CloudNode] cancel job item failed: %v", err)
			return &pb.CancelJobItemRsp{RetInfo: retFromError(err)}, nil
		}
		return &pb.CancelJobItemRsp{RetInfo: retOK()}, nil
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
	var item *pb.JobItemDetail
	var err error
	if s.projectionRepo != nil {
		item, err = s.projectionRepo.Get(ctx, req)
	} else {
		item, err = s.jobItemRepo.Get(ctx, req)
	}
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
	var items []*pb.JobItemDetail
	var page *pb.PageResult
	var err error
	if s.projectionRepo != nil {
		items, page, err = s.projectionRepo.List(ctx, req)
	} else {
		items, page, err = s.jobItemRepo.List(ctx, req)
	}
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
	var attempts []*pb.JobItemAttempt
	var err error
	if s.projectionRepo != nil {
		attempts, err = s.projectionRepo.ListAttempts(ctx, req)
	} else {
		attempts, err = s.jobItemRepo.ListAttempts(ctx, req)
	}
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] list job item attempts failed: %v", err)
		return &pb.ListJobItemAttemptsRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.ListJobItemAttemptsRsp{RetInfo: retOK(), Attempts: attempts}, nil
}

func (s *Service) submitJobItemsWithJetStream(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	acks := make([]*pb.JobItemAck, 0, len(req.GetItems()))
	var created, deduplicated, rejected int32
	for _, item := range req.GetItems() {
		result, err := s.projectionRepo.CreatePending(ctx, item, projection.QueueMeta{})
		if err != nil {
			return &pb.SubmitJobItemsRsp{RetInfo: retFromError(err)}, nil
		}
		ack := &pb.JobItemAck{
			JobItemId:    result.JobItemID,
			Status:       result.Status,
			RejectReason: result.RejectReason,
		}
		if result.Created {
			pub, err := s.executionQueue.Publish(ctx, item)
			if err != nil {
				_ = s.projectionRepo.MarkEnqueueFailed(ctx, item.GetSpaceId(), item.GetJobItemId(), err.Error())
				ack.Status = pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED
				ack.RejectReason = err.Error()
			} else {
				if err := s.projectionRepo.MarkPublished(ctx, item.GetSpaceId(), item.GetJobItemId(), projection.QueueMeta{
					Subject:   pub.Subject,
					Stream:    pub.Stream,
					StreamSeq: pub.Sequence,
				}); err != nil {
					return &pb.SubmitJobItemsRsp{RetInfo: retFromError(err)}, nil
				}
			}
		}
		switch ack.GetStatus() {
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED:
			created++
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED:
			deduplicated++
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED:
			rejected++
		}
		acks = append(acks, ack)
	}
	return &pb.SubmitJobItemsRsp{
		RetInfo:      retOK(),
		Acks:         acks,
		Created:      created,
		Deduplicated: deduplicated,
		Rejected:     rejected,
	}, nil
}

func (s *Service) pollJobItemsWithJetStream(ctx context.Context, req *pb.PollJobItemsReq) ([]*pb.PolledJobItem, error) {
	node, err := s.catalog.GetNode(ctx, req.GetSpaceId(), req.GetNodeId())
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, repository.ErrPollingNodeNotFound
	}
	deliveries, err := s.executionQueue.Fetch(ctx, jobqueue.FetchRequest{
		SpaceID:           req.GetSpaceId(),
		CodePackageID:     node.PackageID,
		SupportedJobTypes: req.GetSupportedJobTypes(),
		Limit:             int(req.GetLimit()),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*pb.PolledJobItem, 0, len(deliveries))
	for _, delivery := range deliveries {
		item, err := s.projectionRepo.GetModel(ctx, delivery.Message.SpaceID, delivery.Message.JobItemID)
		if err != nil {
			_ = s.executionQueue.Term(ctx, delivery.AckSubject)
			continue
		}
		switch item.Status {
		case projection.JobItemStatusCanceled, projection.JobItemStatusSuccess, projection.JobItemStatusFailed:
			_ = s.executionQueue.Term(ctx, delivery.AckSubject)
			continue
		}
		ok, running, err := s.projectionRepo.TryMarkRunning(ctx, projection.RunningRequest{
			SpaceID:    delivery.Message.SpaceID,
			JobItemID:  delivery.Message.JobItemID,
			NodeID:     req.GetNodeId(),
			AckSubject: delivery.AckSubject,
			StreamSeq:  delivery.StreamSeq,
		})
		if err != nil {
			_ = s.executionQueue.Nak(ctx, delivery.AckSubject, time.Second)
			return nil, err
		}
		if !ok {
			_ = s.executionQueue.Nak(ctx, delivery.AckSubject, time.Second)
			continue
		}
		out = append(out, delivery.Message.ToPolledJobItem(running.AttemptNo))
	}
	return out, nil
}

func (s *Service) reportJobItemStatusWithJetStream(ctx context.Context, req *pb.ReportJobItemStatusReq) error {
	item, err := s.projectionRepo.GetModel(ctx, req.GetSpaceId(), req.GetJobItemId())
	if err != nil {
		return err
	}
	if item.Status == projection.JobItemStatusCanceled && item.AttemptNo == int(req.GetAttemptNo()) {
		if item.RunningNode != "" && item.RunningNode != req.GetNodeId() {
			return projection.ErrStaleAttempt
		}
		if err := s.executionQueue.Term(ctx, item.AckSubject); err != nil {
			return err
		}
		return s.projectionRepo.ClearCancelDirective(ctx, req.GetSpaceId(), req.GetJobItemId(), req.GetAttemptNo())
	}
	if item.Status != projection.JobItemStatusRunning {
		return projection.ErrInactive
	}
	if item.RunningNode != req.GetNodeId() || item.AttemptNo != int(req.GetAttemptNo()) {
		return projection.ErrStaleAttempt
	}
	event, err := reportEventFromReq(req)
	if err != nil {
		return err
	}
	if s.projector != nil {
		if err := s.projector.PublishReported(ctx, event); err != nil {
			return err
		}
	} else {
		if err := s.projectionRepo.MarkReportedBatch(ctx, []projection.ReportEvent{event}); err != nil {
			return err
		}
	}
	switch {
	case req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS:
		return s.executionQueue.Ack(ctx, item.AckSubject)
	case req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_CANCELED:
		return s.executionQueue.Term(ctx, item.AckSubject)
	case req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED &&
		req.GetErrorKind() == pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE:
		return s.executionQueue.Nak(ctx, item.AckSubject, time.Second)
	default:
		return s.executionQueue.Term(ctx, item.AckSubject)
	}
}

func reportEventFromReq(req *pb.ReportJobItemStatusReq) (projection.ReportEvent, error) {
	status := ""
	switch req.GetStatus() {
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS:
		status = projection.ReportStatusSuccess
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED:
		status = projection.ReportStatusFailed
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_CANCELED:
		status = projection.ReportStatusCanceled
	default:
		return projection.ReportEvent{}, projection.ErrInvalidJobItem
	}
	errorKind := ""
	switch req.GetErrorKind() {
	case pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE:
		errorKind = projection.ErrorKindRetryable
	case pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT:
		errorKind = projection.ErrorKindPermanent
	}
	result := map[string]any{}
	if req.GetResultSummary() != nil {
		result = req.GetResultSummary().AsMap()
	}
	return projection.ReportEvent{
		SpaceID:       req.GetSpaceId(),
		JobItemID:     req.GetJobItemId(),
		NodeID:        req.GetNodeId(),
		AttemptNo:     req.GetAttemptNo(),
		Status:        status,
		ErrorKind:     errorKind,
		ErrorCode:     req.GetErrorCode(),
		ErrorMessage:  req.GetErrorMessage(),
		ResultSummary: result,
		DurationMS:    req.GetDurationMs(),
		Time:          Now(),
	}, nil
}
