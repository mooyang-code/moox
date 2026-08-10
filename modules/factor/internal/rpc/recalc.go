package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/report"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	if s.viewReadyExecutor != nil {
		// Recalc is a result-period operation, not a best-effort fire-and-forget
		// call. Validate the requested binding before entering the shared
		// executor so a typo cannot return SUCCESS without writing a marker.
		executable, listErr := s.bindings.ListExecutable(ctx)
		if listErr != nil {
			return &factorpb.RecalcFactorRsp{RetInfo: inner(listErr)}, nil
		}
		matching := 0
		for _, binding := range executable {
			if binding.SpaceID != req.GetSpaceId() || binding.SourceViewID != sourceViewID || binding.Freq != req.GetFreq() || !domain.BindingAllowsSubject(binding, req.GetSubjectId()) {
				continue
			}
			if req.GetFactorId() == "" || binding.FactorID == req.GetFactorId() {
				matching++
			}
		}
		if matching == 0 {
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
		var releaseOperation func()
		var gatedExecutor viewReadyExecutorWithGate
		if s.operationGate != nil {
			gatedExecutor, _ = s.viewReadyExecutor.(viewReadyExecutorWithGate)
			if gatedExecutor != nil {
				releaseOperation, err = s.operationGate.AcquireContext(ctx)
				if err != nil {
					return &factorpb.RecalcFactorRsp{RetInfo: inner(err)}, nil
				}
				defer releaseOperation()
			}
		}
		for period := start; period.Before(end); {
			triggerEventID := recalcTriggerEventID(requestID, req, period)
			ready := &publicstoragepb.ViewSourcePeriodReady{SourceViewId: sourceViewID, Frequency: req.GetFreq(), PeriodTime: period.Unix(), Status: "complete", PrimarySubjects: []string{req.GetSubjectId()}, ReadyAt: timestamppb.New(period.UTC())}
			// A Result View is the complete output of its Source View. The
			// executor validates the requested factor under the operation gate,
			// then recalculates the whole group for a coherent result snapshot.
			var executeErr error
			if gatedExecutor != nil {
				executeErr = gatedExecutor.ExecuteSelectedWithGate(ctx, req.GetSpaceId(), triggerEventID, req.GetFactorId(), ready)
			} else {
				executeErr = s.viewReadyExecutor.ExecuteSelected(ctx, req.GetSpaceId(), triggerEventID, req.GetFactorId(), ready)
			}
			if executeErr != nil {
				return &factorpb.RecalcFactorRsp{RetInfo: inner(executeErr)}, nil
			}
			if req.GetFactorId() != "" {
				stillEnabled, checkErr := s.hasRecalcBinding(ctx, req.GetSpaceId(), sourceViewID, req.GetFreq(), req.GetSubjectId(), req.GetFactorId())
				if checkErr != nil {
					return &factorpb.RecalcFactorRsp{RetInfo: inner(checkErr)}, nil
				}
				if !stillEnabled {
					return &factorpb.RecalcFactorRsp{RetInfo: conflict(fmt.Errorf("factor binding %s was disabled during recalc", req.GetFactorId()))}, nil
				}
			}
			next, nextErr := domain.NextPeriod(period, req.GetFreq())
			if nextErr != nil {
				return &factorpb.RecalcFactorRsp{RetInfo: invalid(nextErr)}, nil
			}
			period = next
		}
		return &factorpb.RecalcFactorRsp{RetInfo: success()}, nil
	}
	if s.taskRunner == nil {
		return &factorpb.RecalcFactorRsp{RetInfo: inner(fmt.Errorf("recalc task runner is not configured"))}, nil
	}
	groups, err := s.recalcFactorGroups(ctx, req)
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	targets := make([]string, 0, len(groups))
	for targetDataset := range groups {
		targets = append(targets, targetDataset)
	}
	sort.Strings(targets)
	taskIndex := 0
	for _, targetDataset := range targets {
		factors := groups[targetDataset]
		for _, item := range factors {
			factor := item.Factor
			taskIndex++
			task, buildErr := taskrunner.BuildTask(taskrunner.TaskScope{
				TaskID:      fmt.Sprintf("recalc-%d-%d", time.Now().UnixNano(), taskIndex),
				TriggerType: "recalc", SpaceID: req.GetSpaceId(),
				BindingID: item.BindingID, SourceViewID: req.GetSourceDataset(), ResultDatasetID: targetDataset,
				SubjectID: req.GetSubjectId(), Freq: req.GetFreq(),
				PeriodTime: start.Unix(), TriggerEventID: fmt.Sprintf("recalc-%d", start.UnixNano()), TriggeredAt: time.Now().UTC(),
				StartTime: start, EndTime: end,
			}, factor, s.factorsDir)
			if buildErr != nil {
				return &factorpb.RecalcFactorRsp{RetInfo: invalid(buildErr)}, nil
			}
			if runErr := s.taskRunner.Run(ctx, task); runErr != nil {
				if errors.Is(runErr, taskrunner.ErrStaleTask) {
					return &factorpb.RecalcFactorRsp{RetInfo: conflict(runErr)}, nil
				}
				return &factorpb.RecalcFactorRsp{RetInfo: inner(runErr)}, nil
			}
		}
	}
	return &factorpb.RecalcFactorRsp{RetInfo: success()}, nil
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

func (s *Service) hasRecalcBinding(ctx context.Context, spaceID, sourceViewID, freq, subjectID, factorID string) (bool, error) {
	bindings, err := s.bindings.ListExecutable(ctx)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		if binding.SpaceID == spaceID && binding.SourceViewID == sourceViewID && binding.Freq == freq && binding.FactorID == factorID && domain.BindingAllowsSubject(binding, subjectID) {
			return true, nil
		}
	}
	return false, nil
}

func recalcTriggerEventID(requestID string, req *factorpb.RecalcFactorReq, period time.Time) string {
	parts := []string{requestID}
	if req != nil {
		parts = append(parts, req.GetSpaceId(), req.GetSourceViewId(), req.GetFreq(), req.GetSubjectId(), req.GetFactorId())
	}
	parts = append(parts, period.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "recalc-" + hex.EncodeToString(sum[:16])
}

func legacyRecalcRequestID(req *factorpb.RecalcFactorReq) string {
	if req == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(req.GetFactorId() + "\x00" + req.GetSpaceId() + "\x00" + req.GetSourceDataset() + "\x00" + req.GetSubjectId() + "\x00" + req.GetFreq() + "\x00" + req.GetStartTime() + "\x00" + req.GetEndTime()))
	return "legacy-" + hex.EncodeToString(sum[:16])
}

type recalcBindingFactor struct {
	BindingID string
	Factor    domain.FactorDef
}

func (s *Service) recalcFactorGroups(ctx context.Context, req *factorpb.RecalcFactorReq) (map[string][]recalcBindingFactor, error) {
	bindings, err := s.bindings.ListExecutable(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string][]recalcBindingFactor{}
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		if binding.SpaceID != req.GetSpaceId() ||
			binding.SourceViewID != req.GetSourceDataset() ||
			binding.Freq != req.GetFreq() {
			continue
		}
		if !domain.BindingAllowsSubject(binding, req.GetSubjectId()) {
			continue
		}
		if req.GetFactorId() != "" && binding.FactorID != req.GetFactorId() {
			continue
		}
		if _, ok := seen[binding.FactorID]; ok {
			continue
		}
		factor, loadErr := s.factors.Get(ctx, binding.FactorID)
		if loadErr != nil {
			return nil, fmt.Errorf("load factor %s: %w", binding.FactorID, loadErr)
		}
		if factor.Status != domain.FactorStatusEnabled {
			if req.GetFactorId() != "" {
				return nil, fmt.Errorf("factor %s is not enabled", factor.FactorID)
			}
			continue
		}
		target := binding.ResultDatasetID
		groups[target] = append(groups[target], recalcBindingFactor{BindingID: binding.BindingID, Factor: *factor})
		seen[binding.FactorID] = struct{}{}
	}
	if len(groups) == 0 {
		if req.GetFactorId() != "" {
			return nil, fmt.Errorf("factor %s has no executable binding for the requested source", req.GetFactorId())
		}
		return nil, fmt.Errorf("no executable factors for the requested source")
	}
	return groups, nil
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
