package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/report"
)

func (s *Service) RecalcFactor(ctx context.Context, req *factorpb.RecalcFactorReq) (*factorpb.RecalcFactorRsp, error) {
	sourceViewID := req.GetSourceViewId()
	legacy := sourceViewID == "" && req.GetSourceDataset() != ""
	if sourceViewID == "" {
		sourceViewID = req.GetSourceDataset()
	}
	if req.GetSpaceId() == "" || sourceViewID == "" || req.GetFreq() == "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("space_id, source_view_id and freq are required"))}, nil
	}
	if req.GetSubjectId() == "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("subject_id is required"))}, nil
	}
	start, err := parseRequiredRecalcTime("start_time", req.GetStartTime())
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	end, err := parseRequiredRecalcTime("end_time", req.GetEndTime())
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	if !start.Before(end) {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("start_time must be before end_time"))}, nil
	}
	if err := validateRecalcPeriodRange(start, end, req.GetFreq()); err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	requestID := req.GetRequestId()
	if requestID == "" && legacy {
		requestID = legacyRecalcRequestID(req)
	}
	if requestID == "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("request_id is required"))}, nil
	}
	executable, listErr := s.bindings.ListExecutable(ctx)
	if listErr != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: inner(listErr)}, nil
	}
	matches := make([]domain.FactorBinding, 0, 1)
	for index := range executable {
		binding := executable[index]
		if binding.SpaceID != req.GetSpaceId() || binding.Freq != req.GetFreq() || !domain.BindingAllowsSubject(binding, req.GetSubjectId()) {
			continue
		}
		source := firstNonEmptyRPC(binding.SourceViewID, binding.SourceDataset)
		if source != sourceViewID && source != req.GetSourceDataset() {
			continue
		}
		if req.GetFactorId() != "" && binding.FactorID != req.GetFactorId() {
			continue
		}
		matches = append(matches, binding)
	}
	if len(matches) == 0 {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("no executable factor binding for source_view_id=%s freq=%s subject_id=%s", sourceViewID, req.GetFreq(), req.GetSubjectId()))}, nil
	}
	if req.GetSyncRequestId() != "" {
		if s.meta == nil || s.viewSyncWaiter == nil {
			return &factorpb.RecalcFactorRsp{RetInfo: inner(fmt.Errorf("View sync-point waiter is not configured"))}, nil
		}
		datasetIDs, resolveErr := s.meta.SourceViewDatasetIDs(ctx, req.GetSpaceId(), sourceViewID)
		if resolveErr != nil {
			return &factorpb.RecalcFactorRsp{RetInfo: inner(resolveErr)}, nil
		}
		if waitErr := s.viewSyncWaiter.WaitViewSyncPoint(ctx, req.GetSpaceId(), sourceViewID, req.GetSyncRequestId(), datasetIDs); waitErr != nil {
			return &factorpb.RecalcFactorRsp{RetInfo: inner(waitErr)}, nil
		}
	}
	if _, parseErr := domain.ParseFrequency(req.GetFreq()); parseErr != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(parseErr)}, nil
	}
	if s.meta != nil && s.meta.SupportsViews() {
		if _, indexErr := s.meta.SourceViewActiveIndexID(ctx, req.GetSpaceId(), sourceViewID); indexErr != nil {
			return &factorpb.RecalcFactorRsp{RetInfo: inner(fmt.Errorf("resolve source View active index: %w", indexErr))}, nil
		}
	}
	recalc, err := s.recalcService()
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: inner(err)}, nil
	}
	var accepted trigger.RecalcJob
	for index, binding := range matches {
		jobRequestID := requestID
		if len(matches) > 1 {
			jobRequestID = requestID + "/" + binding.BindingID
		}
		job, acceptErr := recalc.Accept(ctx, trigger.RecalcSpec{
			RequestID: jobRequestID, SpaceID: req.GetSpaceId(), DatasetID: firstNonEmptyRPC(binding.ResultDatasetID, req.GetSourceDataset()),
			SourceViewID: sourceViewID, SubjectID: req.GetSubjectId(), Frequency: req.GetFreq(), FactorID: firstNonEmptyRPC(req.GetFactorId(), binding.FactorID),
			BindingID: binding.BindingID, BindingGeneration: binding.BindingGeneration, StartTime: start, EndTime: end,
		})
		if acceptErr != nil {
			return &factorpb.RecalcFactorRsp{RetInfo: inner(acceptErr)}, nil
		}
		if index == 0 {
			accepted = job
		}
	}
	return &factorpb.RecalcFactorRsp{RetInfo: success(), JobId: accepted.JobID, Status: accepted.Status}, nil
}

func (s *Service) CancelRecalcJob(ctx context.Context, req *factorpb.CancelRecalcJobReq) (*factorpb.CancelRecalcJobRsp, error) {
	recalc, err := s.recalcService()
	if err != nil {
		return &factorpb.CancelRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	if err := recalc.Cancel(ctx, req.GetJobId()); err != nil {
		if errors.Is(err, trigger.ErrRecalcJobNotFound) {
			return &factorpb.CancelRecalcJobRsp{RetInfo: notFound(err)}, nil
		}
		return &factorpb.CancelRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.CancelRecalcJobRsp{RetInfo: success()}, nil
}

func (s *Service) GetRecalcJob(ctx context.Context, req *factorpb.GetRecalcJobReq) (*factorpb.GetRecalcJobRsp, error) {
	recalc, err := s.recalcService()
	if err != nil {
		return &factorpb.GetRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	job, err := recalc.Get(ctx, req.GetJobId())
	if err != nil {
		if errors.Is(err, trigger.ErrRecalcJobNotFound) {
			return &factorpb.GetRecalcJobRsp{RetInfo: notFound(err)}, nil
		}
		return &factorpb.GetRecalcJobRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.GetRecalcJobRsp{
		RetInfo: success(), JobId: job.JobID, RequestId: job.RequestID, Status: job.Status,
		FailureClass: job.FailureClass, Error: job.Error, BindingId: job.BindingID, BindingGeneration: job.BindingGeneration,
	}, nil
}

func validateRecalcPeriodRange(start, end time.Time, frequency string) error {
	for name, value := range map[string]time.Time{"start_time": start, "end_time": end} {
		floored, err := report.RecentDatasetTimes(frequency, value.UTC(), 1)
		if err != nil {
			return err
		}
		if len(floored) != 1 || !floored[0].Equal(value.UTC()) {
			return fmt.Errorf("%s must align to a %s Storage period boundary", name, frequency)
		}
	}
	return nil
}

func legacyRecalcRequestID(req *factorpb.RecalcFactorReq) string {
	if req == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(req.GetFactorId() + "\x00" + req.GetSpaceId() + "\x00" + req.GetSourceDataset() + "\x00" + req.GetSubjectId() + "\x00" + req.GetFreq() + "\x00" + req.GetStartTime() + "\x00" + req.GetEndTime()))
	return "legacy-" + hex.EncodeToString(sum[:16])
}

func parseRequiredRecalcTime(name, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q must be RFC3339", name, raw)
	}
	return value.UTC(), nil
}

func firstNonEmptyRPC(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func notFound(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: err.Error()}
}
