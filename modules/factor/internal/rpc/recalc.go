package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

type recalcState struct {
	RecalcID string
	Status   string
	Total    int32
	Finished int32
	Error    string
}

func (s *Service) RecalcFactor(ctx context.Context, req *factorpb.RecalcFactorReq) (*factorpb.RecalcFactorRsp, error) {
	if req.GetSpaceId() == "" || req.GetSourceDataset() == "" || req.GetFreq() == "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("space_id, source_dataset and freq are required"))}, nil
	}
	if req.GetSubjectId() == "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("subject_id is required for V1 recalc"))}, nil
	}
	if s.scheduler == nil {
		return &factorpb.RecalcFactorRsp{RetInfo: inner(fmt.Errorf("recalc scheduler is not configured"))}, nil
	}
	barTime, err := recalcEndTime(req.GetEndTime())
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	if req.GetStartTime() != "" {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(fmt.Errorf("start_time range recalc is not supported in V1; use end_time for a single bar"))}, nil
	}
	tasks, err := s.recalcTasks(ctx, req, barTime)
	if err != nil {
		return &factorpb.RecalcFactorRsp{RetInfo: invalid(err)}, nil
	}
	recalcID := fmt.Sprintf("recalc-%d", time.Now().UnixNano())
	results := make(chan scheduler.TaskResult, len(tasks))
	s.setRecalcState(&recalcState{RecalcID: recalcID, Status: "queued", Total: int32(len(tasks))})
	for i := range tasks {
		tasks[i].TaskID = fmt.Sprintf("%s-%d", recalcID, i+1)
		tasks[i].Completion = results
		s.scheduler.Enqueue(ctx, tasks[i])
	}
	go s.drainRecalc(recalcID, results, len(tasks))
	return &factorpb.RecalcFactorRsp{RetInfo: success(), RecalcId: recalcID}, nil
}

func (s *Service) GetRecalcProgress(_ context.Context, req *factorpb.GetRecalcProgressReq) (*factorpb.GetRecalcProgressRsp, error) {
	if req.GetRecalcId() == "" {
		return &factorpb.GetRecalcProgressRsp{RetInfo: invalid(fmt.Errorf("recalc_id is required"))}, nil
	}
	state := s.getRecalcState(req.GetRecalcId())
	if state == nil {
		return &factorpb.GetRecalcProgressRsp{RetInfo: invalid(fmt.Errorf("recalc_id %s not found", req.GetRecalcId()))}, nil
	}
	status := state.Status
	if state.Error != "" {
		status = status + ": " + state.Error
	}
	return &factorpb.GetRecalcProgressRsp{
		RetInfo:  success(),
		RecalcId: state.RecalcID,
		Status:   status,
		Total:    state.Total,
		Finished: state.Finished,
	}, nil
}

func (s *Service) recalcTasks(ctx context.Context, req *factorpb.RecalcFactorReq, barTime time.Time) ([]scheduler.Task, error) {
	if req.GetFactorId() != "" {
		factor, err := s.factors.Get(ctx, req.GetFactorId())
		if err != nil {
			return nil, fmt.Errorf("load factor %s: %w", req.GetFactorId(), err)
		}
		if factor.Kind != domain.FactorKindTimeseries {
			return nil, fmt.Errorf("factor %s is not a timeseries factor", factor.FactorID)
		}
		targetDataset, err := s.targetDatasetForFactor(ctx, req.GetSpaceId(), req.GetSourceDataset(), req.GetFreq(), req.GetFactorId())
		if err != nil {
			return nil, err
		}
		return []scheduler.Task{s.recalcTask(req, barTime, targetDataset, []domain.FactorDef{*factor})}, nil
	}

	bindings, err := s.bindings.ListEnabledBySource(ctx, req.GetSpaceId(), req.GetSourceDataset(), req.GetFreq())
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, fmt.Errorf("no enabled factor bindings for %s/%s/%s", req.GetSpaceId(), req.GetSourceDataset(), req.GetFreq())
	}
	seen := map[string]struct{}{}
	grouped := map[string][]domain.FactorDef{}
	order := []string{}
	for _, binding := range bindings {
		if _, ok := seen[binding.FactorID]; ok {
			continue
		}
		factor, err := s.factors.Get(ctx, binding.FactorID)
		if err != nil {
			return nil, fmt.Errorf("load factor %s: %w", binding.FactorID, err)
		}
		if factor.Status != domain.FactorStatusEnabled || factor.Kind != domain.FactorKindTimeseries {
			continue
		}
		targetDataset := binding.TargetDataset
		if targetDataset == "" {
			targetDataset = registry.ResultDataset(req.GetSourceDataset())
		}
		if _, ok := grouped[targetDataset]; !ok {
			order = append(order, targetDataset)
		}
		grouped[targetDataset] = append(grouped[targetDataset], *factor)
		seen[binding.FactorID] = struct{}{}
	}
	tasks := make([]scheduler.Task, 0, len(order))
	for _, targetDataset := range order {
		tasks = append(tasks, s.recalcTask(req, barTime, targetDataset, grouped[targetDataset]))
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no enabled timeseries factors for %s/%s/%s", req.GetSpaceId(), req.GetSourceDataset(), req.GetFreq())
	}
	return tasks, nil
}

func (s *Service) recalcTask(req *factorpb.RecalcFactorReq, barTime time.Time, targetDataset string, factors []domain.FactorDef) scheduler.Task {
	specs := make([]engine.FactorSpec, 0, len(factors))
	ids := make([]string, 0, len(factors))
	lookback := 0
	for _, factor := range factors {
		sourcePath := filepath.Join(s.factorsDir, ".versions", "factor", factor.Name, factor.SourceHash, "module.py")
		specs = append(specs, engine.FactorSpec{
			FactorID:      factor.FactorID,
			Name:          factor.Name,
			SourceHash:    factor.SourceHash,
			SourcePath:    sourcePath,
			Params:        recalcParams(factor.ParamsJSON),
			EstimatedMS:   int64(factor.AvgRuntimeMS),
			WritebackBars: factor.WritebackBars,
			ExtraColumns:  registry.ExtraColumnsFromFactors([]domain.FactorDef{factor}),
		})
		ids = append(ids, factor.FactorID)
		if factor.LookbackBars > lookback {
			lookback = factor.LookbackBars
		}
	}
	if lookback <= 0 {
		lookback = registry.DefaultLookback(nil)
	}
	if targetDataset == "" {
		targetDataset = registry.ResultDataset(req.GetSourceDataset())
	}
	return scheduler.Task{
		FactorTask: engine.FactorTask{
			Kind:          domain.FactorKindTimeseries,
			SpaceID:       req.GetSpaceId(),
			SourceDataset: req.GetSourceDataset(),
			TargetDataset: targetDataset,
			SubjectID:     req.GetSubjectId(),
			Freq:          req.GetFreq(),
			BarTime:       barTime,
			LookbackBars:  lookback,
			Factors:       specs,
		},
		TriggerType: "recalc",
		FactorIDs:   ids,
	}
}

func (s *Service) targetDatasetForFactor(ctx context.Context, spaceID string, sourceDataset string, freq string, factorID string) (string, error) {
	bindings, err := s.bindings.ListEnabledBySource(ctx, spaceID, sourceDataset, freq)
	if err != nil {
		return "", err
	}
	for _, binding := range bindings {
		if binding.FactorID != factorID {
			continue
		}
		if binding.TargetDataset != "" {
			return binding.TargetDataset, nil
		}
		break
	}
	return registry.ResultDataset(sourceDataset), nil
}

func (s *Service) drainRecalc(recalcID string, results <-chan scheduler.TaskResult, total int) {
	s.updateRecalcState(recalcID, func(state *recalcState) {
		state.Status = "running"
	})
	_ = s.scheduler.Drain(context.Background())
	failures := make([]string, 0)
	for i := 0; i < total; i++ {
		result := <-results
		if result.Status == domain.RunStatusFailed {
			failures = append(failures, resultErrorMessage(result))
		}
		s.updateRecalcState(recalcID, func(state *recalcState) {
			state.Finished++
		})
	}
	s.updateRecalcState(recalcID, func(state *recalcState) {
		if len(failures) > 0 {
			state.Status = "failed"
			state.Error = strings.Join(failures, "; ")
			return
		}
		state.Status = "succeeded"
	})
}

func resultErrorMessage(result scheduler.TaskResult) string {
	if result.Error != nil {
		return result.Error.Error()
	}
	if result.ErrorMessage != "" {
		return result.ErrorMessage
	}
	return fmt.Sprintf("task %s failed", result.TaskID)
}

func (s *Service) setRecalcState(state *recalcState) {
	s.recalcMu.Lock()
	defer s.recalcMu.Unlock()
	s.recalc[state.RecalcID] = state
}

func (s *Service) getRecalcState(recalcID string) *recalcState {
	s.recalcMu.Lock()
	defer s.recalcMu.Unlock()
	state := s.recalc[recalcID]
	if state == nil {
		return nil
	}
	cp := *state
	return &cp
}

func (s *Service) updateRecalcState(recalcID string, update func(*recalcState)) {
	s.recalcMu.Lock()
	defer s.recalcMu.Unlock()
	state := s.recalc[recalcID]
	if state == nil {
		return
	}
	update(state)
}

func recalcEndTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Now().UTC(), nil
	}
	return parseRecalcTime(raw)
}

func parseRecalcTime(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q must be RFC3339", raw)
	}
	return t.UTC(), nil
}

func recalcParams(raw string) []int {
	var params []int
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil
	}
	return params
}
