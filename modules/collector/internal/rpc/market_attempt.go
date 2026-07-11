package rpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func (s *Service) GetMarketAttemptReceipt(ctx context.Context, req *pb.GetMarketAttemptReceiptReq) (*pb.GetMarketAttemptReceiptRsp, error) {
	if req.GetJobItemId() == "" || req.GetAttemptNo() <= 0 {
		return &pb.GetMarketAttemptReceiptRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id and attempt_no are required")}, nil
	}
	receipt, err := s.attempts.GetReceipt(ctx, req.GetJobItemId(), req.GetAttemptNo())
	if err == gorm.ErrRecordNotFound {
		return &pb.GetMarketAttemptReceiptRsp{RetInfo: retOK(), Found: false}, nil
	}
	if err != nil {
		return &pb.GetMarketAttemptReceiptRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.GetMarketAttemptReceiptRsp{RetInfo: retOK(), Found: true, Receipt: attemptReceiptPB(receipt)}, nil
}

func (s *Service) FinalizeMarketAttempt(ctx context.Context, req *pb.FinalizeMarketAttemptReq) (*pb.FinalizeMarketAttemptRsp, error) {
	if req.GetJobItemId() == "" || req.GetAttemptNo() <= 0 || req.GetSpaceId() == "" {
		return &pb.FinalizeMarketAttemptRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_item_id, attempt_no and space_id are required")}, nil
	}
	windowStart, err := optionalRFC3339(req.GetWindowStart())
	if err != nil {
		return &pb.FinalizeMarketAttemptRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid window_start")}, nil
	}
	windowEnd, err := optionalRFC3339(req.GetWindowEnd())
	if err != nil {
		return &pb.FinalizeMarketAttemptRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid window_end")}, nil
	}
	summary, _ := json.Marshal(req.GetSummary().AsMap())
	subjects := make([]domain.AttemptSubject, 0, len(req.GetSubjects()))
	for _, value := range req.GetSubjects() {
		subjects = append(subjects, domain.AttemptSubject{TaskID: value.GetTaskId(), SubjectID: value.GetSubjectId(), Status: value.GetStatus(), NextCandidateIndex: int(value.GetNextCandidateIndex()), Rows: value.GetRows(), ErrorClass: value.GetErrorClass()})
	}
	outbox := make([]domain.AttemptOutbox, 0, len(req.GetOutbox()))
	for _, value := range req.GetOutbox() {
		raw, _ := json.Marshal(value.GetPayload().AsMap())
		outbox = append(outbox, domain.AttemptOutbox{Kind: value.GetKind(), Payload: string(raw)})
	}
	receipt, err := s.attempts.Finalize(ctx, repository.FinalizeAttemptRequest{Attempt: domain.MarketAttempt{JobItemID: req.GetJobItemId(), AttemptNo: req.GetAttemptNo(), PlanID: req.GetPlanId(), MarketID: req.GetMarketId(), SpaceID: req.GetSpaceId(), ProviderID: req.GetProviderId(), Feed: req.GetFeed(), Phase: req.GetPhase(), WindowStart: windowStart, WindowEnd: windowEnd, Cursor: req.GetCursor(), Status: req.GetStatus(), Summary: string(summary), ErrorClass: req.GetErrorClass()}, Subjects: subjects, Outbox: outbox, Now: time.Now().UTC()})
	if err != nil {
		return &pb.FinalizeMarketAttemptRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.FinalizeMarketAttemptRsp{RetInfo: retOK(), Receipt: attemptReceiptPB(receipt), AlreadyFinalized: receipt.AlreadyFinalized}, nil
}

func attemptReceiptPB(value repository.AttemptReceipt) *pb.MarketAttemptReceipt {
	summaryMap := map[string]any{}
	_ = json.Unmarshal([]byte(value.Attempt.Summary), &summaryMap)
	summary, _ := structpb.NewStruct(summaryMap)
	result := &pb.MarketAttemptReceipt{JobItemId: value.Attempt.JobItemID, AttemptNo: value.Attempt.AttemptNo, MarketId: value.Attempt.MarketID, SpaceId: value.Attempt.SpaceID, ProviderId: value.Attempt.ProviderID, Feed: value.Attempt.Feed, Phase: value.Attempt.Phase, Status: value.Attempt.Status, Summary: summary, ErrorClass: value.Attempt.ErrorClass, Finalized: value.Attempt.Finalized}
	if value.Attempt.FinalizedAt != nil {
		result.FinalizedAt = value.Attempt.FinalizedAt.UTC().Format(time.RFC3339Nano)
	}
	for _, subject := range value.Subjects {
		result.Subjects = append(result.Subjects, &pb.MarketAttemptSubject{TaskId: subject.TaskID, SubjectId: subject.SubjectID, Status: subject.Status, NextCandidateIndex: int32(subject.NextCandidateIndex), Rows: subject.Rows, ErrorClass: subject.ErrorClass})
	}
	return result
}
func optionalRFC3339(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	return value.UTC(), err
}
