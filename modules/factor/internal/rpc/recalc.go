package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

func (s *Service) RecalcFactor(ctx context.Context, req *factorpb.RecalcFactorReq) (*factorpb.RecalcFactorRsp, error) {
	if req.GetSpaceId() == "" || req.GetSourceDataset() == "" || req.GetFreq() == "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("space_id, source_dataset and freq are required"))}, nil
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
	if s.scheduler == nil {
		return &factorpb.RecalcFactorRsp{RetInfo: inner(fmt.Errorf("recalc scheduler is not configured"))}, nil
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
		for _, factor := range factors {
			taskIndex++
			task, buildErr := scheduler.BuildTask(scheduler.TaskScope{
				TaskID:      fmt.Sprintf("recalc-%d-%d", time.Now().UnixNano(), taskIndex),
				TriggerType: "recalc", SpaceID: req.GetSpaceId(),
				SourceDataset: req.GetSourceDataset(), TargetDataset: targetDataset,
				SubjectID: req.GetSubjectId(), Freq: req.GetFreq(),
				StartTime: start, EndTime: end,
			}, factor, s.factorsDir)
			if buildErr != nil {
				return &factorpb.RecalcFactorRsp{RetInfo: invalid(buildErr)}, nil
			}
			if runErr := s.scheduler.Run(ctx, task); runErr != nil {
				if errors.Is(runErr, scheduler.ErrStaleTask) {
					return &factorpb.RecalcFactorRsp{RetInfo: conflict(runErr)}, nil
				}
				return &factorpb.RecalcFactorRsp{RetInfo: inner(runErr)}, nil
			}
		}
	}
	return &factorpb.RecalcFactorRsp{RetInfo: success()}, nil
}

func (s *Service) recalcFactorGroups(ctx context.Context, req *factorpb.RecalcFactorReq) (map[string][]domain.FactorDef, error) {
	bindings, err := s.bindings.ListExecutable(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string][]domain.FactorDef{}
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		if binding.SpaceID != req.GetSpaceId() ||
			binding.SourceDataset != req.GetSourceDataset() ||
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
		target := binding.TargetDataset
		if target == "" {
			target = registry.ResultDataset(req.GetSourceDataset())
		}
		groups[target] = append(groups[target], *factor)
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
