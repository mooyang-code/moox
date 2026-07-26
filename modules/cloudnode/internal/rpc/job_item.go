package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
)

var errJobItemStateNotConfigured = errors.New("job item state store is not configured")

func (s *Service) SubmitJobItems(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	if s.executionQueue == nil || s.jobState == nil {
		return &pb.SubmitJobItemsRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
	}
	return s.submitJobItems(ctx, req)
}

func (s *Service) submitJobItems(ctx context.Context, req *pb.SubmitJobItemsReq) (*pb.SubmitJobItemsRsp, error) {
	for _, item := range req.GetItems() {
		if err := jobstate.ValidateJobItem(item); err != nil {
			return &pb.SubmitJobItemsRsp{RetInfo: retFromError(err)}, nil
		}
	}
	acks := make([]*pb.JobItemAck, 0, len(req.GetItems()))
	ensured := make(map[string]struct{})
	var created, deduplicated, rejected int32
	for _, item := range req.GetItems() {
		if executeAt := item.GetExecuteAt(); executeAt != nil {
			if err := executeAt.CheckValid(); err != nil {
				acks = append(acks, &pb.JobItemAck{
					JobItemId:    item.GetJobItemId(),
					Status:       pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED,
					RejectReason: "invalid execute_at: " + err.Error(),
				})
				rejected++
				continue
			}
		}
		key := item.GetSpaceId() + "\x00" + item.GetJobType()
		if _, ok := ensured[key]; !ok {
			if err := s.executionQueue.EnsureJobExecutionQueue(ctx, cloudjobqueue.Identity{SpaceID: item.GetSpaceId(), JobType: item.GetJobType()}); err != nil {
				return &pb.SubmitJobItemsRsp{RetInfo: retFromError(err)}, nil
			}
			ensured[key] = struct{}{}
		}
		result, err := s.jobState.CreatePending(ctx, item)
		if err != nil {
			return &pb.SubmitJobItemsRsp{RetInfo: retFromError(err)}, nil
		}
		ack := &pb.JobItemAck{JobItemId: result.JobItemID, Status: result.Status, RejectReason: result.RejectReason}
		if result.ShouldPublish {
			if err := s.executionQueue.Publish(ctx, item); err != nil {
				_ = report.ObserveModuleRun("cloudnode", "dispatch", "error", "cloudnode-jobs", time.Now())
				if stateErr := s.jobState.MarkEnqueueFailed(ctx, item.GetSpaceId(), item.GetJobItemId(), err.Error()); stateErr != nil {
					return &pb.SubmitJobItemsRsp{RetInfo: retFromError(errors.Join(err, stateErr))}, nil
				}
				ack.Status = pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED
				ack.RejectReason = err.Error()
			} else {
				_ = report.ObserveModuleRun("cloudnode", "dispatch", "success", "cloudnode-jobs", time.Now().UTC())
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
	return &pb.SubmitJobItemsRsp{RetInfo: retOK(), Acks: acks, Created: created, Deduplicated: deduplicated, Rejected: rejected}, nil
}

func (s *Service) ReportJobItemStatus(ctx context.Context, req *pb.ReportJobItemStatusReq) (*pb.ReportJobItemStatusRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetNodeId()) == "" || strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.ReportJobItemStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id, node_id and job_item_id are required")}, nil
	}
	event, err := jobStateReportEventFromReq(req)
	if err != nil {
		return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(err)}, nil
	}
	if s.jobState == nil {
		return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
	}
	state, first, err := s.jobState.MarkReported(ctx, event)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] report job item status failed: %v", err)
		return &pb.ReportJobItemStatusRsp{RetInfo: retFromError(err)}, nil
	}
	if first && state != nil && s.history != nil {
		if err := s.history.WriteTerminal(ctx, *state); err != nil {
			log.WarnContextf(ctx, "cloudnode_job_history_write_failed space_id=%s job_item_id=%s err=%v", state.SpaceID, state.JobItemID, err)
		}
	}
	return &pb.ReportJobItemStatusRsp{RetInfo: retOK()}, nil
}

func (s *Service) GetJobItem(ctx context.Context, req *pb.GetJobItemReq) (*pb.GetJobItemRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetJobItemId()) == "" {
		return &pb.GetJobItemRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and job_item_id are required")}, nil
	}
	if s.jobState == nil {
		return &pb.GetJobItemRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
	}
	state, err := s.jobState.Get(ctx, req.GetSpaceId(), req.GetJobItemId())
	if err != nil {
		return &pb.GetJobItemRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.GetJobItemRsp{RetInfo: retOK(), Item: state.ToDetail()}, nil
}

func (s *Service) ListJobItems(ctx context.Context, req *pb.ListJobItemsReq) (*pb.ListJobItemsRsp, error) {
	if s.jobState == nil {
		return &pb.ListJobItemsRsp{RetInfo: retFromError(errJobItemStateNotConfigured)}, nil
	}
	items, page, err := s.jobState.List(ctx, req)
	if err != nil {
		return &pb.ListJobItemsRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.ListJobItemsRsp{RetInfo: retOK(), Items: items, Page: page}, nil
}

func jobStateReportEventFromReq(req *pb.ReportJobItemStatusReq) (jobstate.ReportEvent, error) {
	status := ""
	switch req.GetStatus() {
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS:
		status = jobstate.StatusSuccess
	case pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED:
		status = jobstate.StatusFailed
	default:
		return jobstate.ReportEvent{}, jobstate.ErrInvalid
	}
	if status == jobstate.StatusFailed && req.GetErrorKind() == pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED {
		return jobstate.ReportEvent{}, jobstate.ErrInvalid
	}
	result := map[string]any{}
	if req.GetResultSummary() != nil {
		result = req.GetResultSummary().AsMap()
	}
	return jobstate.ReportEvent{
		SpaceID: req.GetSpaceId(), JobItemID: req.GetJobItemId(), NodeID: req.GetNodeId(), Status: status,
		ErrorKind: jobstate.ErrorKindFromPB(req.GetErrorKind()), ErrorCode: req.GetErrorCode(),
		ErrorMessage: req.GetErrorMessage(), ResultSummary: result, DurationMS: req.GetDurationMs(), Time: Now(),
	}, nil
}
