package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

const supportedJobItemProtocolVersion = "cloudnode-jobitem-v1"

var errJobItemStateNotConfigured = errors.New("job item state store is not configured")

func (s *Service) SubmitJobItems(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	if s.executionQueue != nil && s.jobState != nil {
		return s.submitJobItemsWithActiveKV(ctx, req)
	}
	return &pb.SubmitJobItemsRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
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
	if s.executionQueue != nil && s.jobState != nil {
		items, err := s.pollJobItemsWithActiveKV(ctx, req)
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
	return &pb.PollJobItemsRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
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
	if s.executionQueue != nil && s.jobState != nil {
		if err := s.reportJobItemStatusWithActiveKV(ctx, req); err != nil {
			log.ErrorContextf(ctx, "[CloudNode] report job item status failed: %v", err)
			return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(err)}, nil
		}
		return &pb.ReportJobItemStatusRsp{RetInfo: retOK()}, nil
	}
	return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
}

func (s *Service) CancelJobItem(ctx context.Context, req *pb.CancelJobItemReq) (*pb.CancelJobItemRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.CancelJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.CancelJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id is required")}, nil
	}
	if s.jobState != nil {
		if err := s.jobState.MarkCanceled(ctx, req.GetSpaceId(), req.GetJobItemId(), "user canceled"); err != nil {
			log.ErrorContextf(ctx, "[CloudNode] cancel job item failed: %v", err)
			return &pb.CancelJobItemRsp{RetInfo: retFromError(err)}, nil
		}
		if state, err := s.jobState.Get(ctx, req.GetSpaceId(), req.GetJobItemId()); err == nil && state.IsTerminal() {
			s.writeTerminalHistory(ctx, *state)
		}
		return &pb.CancelJobItemRsp{RetInfo: retOK()}, nil
	}
	return &pb.CancelJobItemRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
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
	if s.jobState != nil {
		state, err := s.jobState.Get(ctx, req.GetSpaceId(), req.GetJobItemId())
		if err == nil {
			item = state.ToDetail()
		}
	} else {
		err = errJobItemStateNotConfigured
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
	if s.jobState != nil {
		items, page, err = s.jobState.List(ctx, req)
	} else {
		err = errJobItemStateNotConfigured
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
	if s.jobState != nil {
		attempts, err = s.jobState.ListAttempts(ctx, req)
	} else {
		err = errJobItemStateNotConfigured
	}
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] list job item attempts failed: %v", err)
		return &pb.ListJobItemAttemptsRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.ListJobItemAttemptsRsp{RetInfo: retOK(), Attempts: attempts}, nil
}

func (s *Service) submitJobItemsWithActiveKV(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	acks := make([]*pb.JobItemAck, 0, len(req.GetItems()))
	var created, deduplicated, rejected int32
	for _, item := range req.GetItems() {
		result, err := s.jobState.CreatePending(ctx, item, jobstate.QueueMeta{})
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
				_ = s.jobState.MarkEnqueueFailed(ctx, item.GetSpaceId(), item.GetJobItemId(), err.Error())
				ack.Status = pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED
				ack.RejectReason = err.Error()
			} else {
				if err := s.jobState.MarkPublished(ctx, item.GetSpaceId(), item.GetJobItemId(), jobstate.QueueMeta{
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

func (s *Service) pollJobItemsWithActiveKV(ctx context.Context, req *pb.PollJobItemsReq) ([]*pb.PolledJobItem, error) {
	node, err := s.catalog.GetNode(ctx, req.GetSpaceId(), req.GetNodeId())
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, store.ErrPollingNodeNotFound
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
		state, err := s.jobState.Get(ctx, delivery.Message.SpaceID, delivery.Message.JobItemID)
		if errors.Is(err, jobstate.ErrNotFound) {
			log.WarnContextf(ctx, "cloudnode_job_orphan space_id=%s job_item_id=%s stream_seq=%d action=term",
				delivery.Message.SpaceID, delivery.Message.JobItemID, delivery.StreamSeq)
			_ = s.executionQueue.Term(ctx, delivery.AckSubject)
			continue
		}
		if err != nil {
			_ = s.executionQueue.Nak(ctx, delivery.AckSubject, time.Second)
			return nil, err
		}
		if state.IsTerminal() || state.Status == jobstate.StatusCanceled {
			s.writeTerminalHistory(ctx, *state)
			_ = s.executionQueue.Term(ctx, delivery.AckSubject)
			continue
		}
		ok, running, err := s.jobState.TryMarkRunning(ctx, jobstate.RunningRequest{
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
			latest, getErr := s.jobState.Get(ctx, delivery.Message.SpaceID, delivery.Message.JobItemID)
			if getErr == nil && latest.IsTerminal() {
				s.writeTerminalHistory(ctx, *latest)
				_ = s.executionQueue.Term(ctx, delivery.AckSubject)
				continue
			}
			_ = s.executionQueue.Nak(ctx, delivery.AckSubject, time.Second)
			continue
		}
		out = append(out, delivery.Message.ToPolledJobItem(running.AttemptNo))
	}
	return out, nil
}

func (s *Service) reportJobItemStatusWithActiveKV(ctx context.Context, req *pb.ReportJobItemStatusReq) error {
	state, err := s.jobState.Get(ctx, req.GetSpaceId(), req.GetJobItemId())
	if err != nil {
		return err
	}
	ackSubject := state.Queue.AckSubject
	if state.Status == jobstate.StatusCanceled && state.AttemptNo == int(req.GetAttemptNo()) {
		if state.RunningNode != "" && state.RunningNode != req.GetNodeId() {
			return jobstate.ErrStaleAttempt
		}
		event, err := jobStateReportEventFromReq(req)
		if err != nil {
			return err
		}
		event.Status = jobstate.StatusCanceled
		updated, err := s.jobState.MarkReported(ctx, event)
		if err != nil && !errors.Is(err, jobstate.ErrInactive) {
			return err
		}
		if updated != nil {
			s.writeTerminalHistory(ctx, *updated)
		}
		if err := s.executionQueue.Term(ctx, ackSubject); err != nil {
			return err
		}
		return s.jobState.ClearCancelDirective(ctx, req.GetSpaceId(), req.GetJobItemId(), req.GetAttemptNo())
	}
	if state.Status != jobstate.StatusRunning {
		return jobstate.ErrInactive
	}
	if state.RunningNode != req.GetNodeId() || state.AttemptNo != int(req.GetAttemptNo()) {
		return jobstate.ErrStaleAttempt
	}
	event, err := jobStateReportEventFromReq(req)
	if err != nil {
		return err
	}
	updated, err := s.jobState.MarkReported(ctx, event)
	if err != nil {
		return err
	}
	if updated.IsTerminal() {
		s.writeTerminalHistory(ctx, *updated)
	}
	switch {
	case req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS:
		return s.executionQueue.Ack(ctx, ackSubject)
	case req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_CANCELED:
		return s.executionQueue.Term(ctx, ackSubject)
	case req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED &&
		req.GetErrorKind() == pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE &&
		updated.Status == jobstate.StatusPending:
		return s.executionQueue.Nak(ctx, ackSubject, time.Second)
	default:
		return s.executionQueue.Term(ctx, ackSubject)
	}
}

func (s *Service) writeTerminalHistory(ctx context.Context, state jobstate.State) {
	if s.history == nil || !state.IsTerminal() {
		return
	}
	if err := s.history.WriteTerminal(ctx, state); err != nil {
		log.WarnContextf(ctx, "cloudnode_job_history_write_failed space_id=%s job_item_id=%s err=%v",
			state.SpaceID, state.JobItemID, err)
		return
	}
	if err := s.jobState.MarkHistorySynced(ctx, state.SpaceID, state.JobItemID); err != nil {
		log.WarnContextf(ctx, "cloudnode_job_history_mark_synced_failed space_id=%s job_item_id=%s err=%v",
			state.SpaceID, state.JobItemID, err)
	}
}

func jobStateReportEventFromReq(req *pb.ReportJobItemStatusReq) (jobstate.ReportEvent, error) {
	status := ""
	switch req.GetStatus() {
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS:
		status = jobstate.StatusSuccess
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED:
		status = jobstate.StatusFailed
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_CANCELED:
		status = jobstate.StatusCanceled
	default:
		return jobstate.ReportEvent{}, jobstate.ErrInvalid
	}
	errorKind := jobstate.ErrorKindFromPB(req.GetErrorKind())
	result := map[string]any{}
	if req.GetResultSummary() != nil {
		result = req.GetResultSummary().AsMap()
	}
	return jobstate.ReportEvent{
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
