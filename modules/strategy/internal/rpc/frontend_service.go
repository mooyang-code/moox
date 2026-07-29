package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	trpc "trpc.group/trpc-go/trpc-go"
)

func (s *Service) GetStrategy(
	ctx context.Context,
	req *strategypb.GetStrategyReq,
) (*strategypb.GetStrategyRsp, error) {
	if req == nil || req.GetStrategyId() == "" || s.Repo == nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(errors.New("strategy_id is required"))}, nil
	}
	value, err := s.Repo.GetStrategy(ctx, req.GetStrategyId())
	if err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyRsp{RetInfo: success(), Strategy: strategyProto(value)}, nil
}

func (s *Service) ListStrategies(
	ctx context.Context,
	req *strategypb.ListStrategiesReq,
) (*strategypb.ListStrategiesRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListStrategiesRsp{
			RetInfo: invalid(errors.New("strategy repository is unavailable")),
		}, nil
	}
	values, err := s.Repo.ListStrategies(ctx)
	if err != nil {
		return &strategypb.ListStrategiesRsp{RetInfo: invalid(err)}, nil
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.Strategy, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, strategyProto(value))
	}
	return &strategypb.ListStrategiesRsp{
		RetInfo: success(), Strategies: items, Total: int64(len(values)),
		Page: int32(page), PageSize: int32(size),
	}, nil
}

func (s *Service) GetRunner(
	ctx context.Context,
	req *strategypb.GetRunnerReq,
) (*strategypb.GetRunnerRsp, error) {
	if req == nil || req.GetRunnerId() == "" || s.Repo == nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(errors.New("runner_id is required"))}, nil
	}
	value, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, value); err != nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetRunnerRsp{RetInfo: success(), Runner: runnerProto(value)}, nil
}

func (s *Service) ListRunners(
	ctx context.Context,
	req *strategypb.ListRunnersReq,
) (*strategypb.ListRunnersRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListRunnersRsp{
			RetInfo: invalid(errors.New("strategy repository is unavailable")),
		}, nil
	}
	filter := store.RunnerFilter{}
	if req != nil {
		filter.StrategyID = req.GetStrategyId()
		filter.SpaceID = req.GetSpaceId()
		filter.Status = domain.RunnerStatus(req.GetStatus())
	}
	if scoped := requestSpaceID(ctx); scoped != "" {
		filter.SpaceID = scoped
	}
	values, err := s.Repo.ListRunners(ctx, filter)
	if err != nil {
		return &strategypb.ListRunnersRsp{RetInfo: invalid(err)}, nil
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.StrategyRunner, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, runnerProto(value))
	}
	return &strategypb.ListRunnersRsp{
		RetInfo: success(), Runners: items, Total: int64(len(values)),
		Page: int32(page), PageSize: int32(size),
	}, nil
}

func (s *Service) UpdateRunner(
	ctx context.Context,
	req *strategypb.UpdateRunnerReq,
) (*strategypb.UpdateRunnerRsp, error) {
	if req == nil || req.GetRunner() == nil || s.Repo == nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(errors.New("runner is required"))}, nil
	}
	value := req.GetRunner()
	if err := validateRunnerInput(value); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	current, err := s.Repo.GetRunner(ctx, value.GetRunnerId())
	if err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, current); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if current.Status != domain.RunnerStatusDisabled {
		return &strategypb.UpdateRunnerRsp{
			RetInfo: invalid(store.ErrRunnerEnabled),
		}, nil
	}
	if value.GetSpaceId() != current.SpaceID {
		return &strategypb.UpdateRunnerRsp{
			RetInfo: invalid(errors.New("runner space_id is immutable")),
		}, nil
	}
	if value.GetLogicalAccountId() != "" {
		if s.LogicalAccounts == nil {
			return &strategypb.UpdateRunnerRsp{
				RetInfo: invalid(errors.New("logical account owner client is unavailable")),
			}, nil
		}
		if err := s.LogicalAccounts.Validate(
			ctx,
			current.SpaceID,
			value.GetLogicalAccountId(),
		); err != nil {
			return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	if current.LogicalAccountID != nil &&
		dereference(current.LogicalAccountID) != value.GetLogicalAccountId() {
		if err := s.releaseRunner(ctx, current); err != nil {
			return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	now := s.now()
	updated := domain.StrategyRunner{
		ID: current.ID, StrategyID: value.GetStrategyId(), SpaceID: current.SpaceID,
		ViewID: value.GetViewId(), Frequency: value.GetFrequency(),
		ParamsJSON:       json.RawMessage(value.GetParamsJson()),
		LogicalAccountID: optionalString(value.GetLogicalAccountId()),
		UpdatedAt:        now,
	}
	if updated.StrategyID != current.StrategyID {
		if _, err := s.Repo.GetStrategy(ctx, updated.StrategyID); err != nil {
			return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	if err := s.Repo.UpdateRunner(ctx, updated); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	result, err := s.Repo.GetRunner(ctx, current.ID)
	if err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.UpdateRunnerRsp{RetInfo: success(), Runner: runnerProto(result)}, nil
}

func (s *Service) SetRunnerStatus(
	ctx context.Context,
	req *strategypb.SetRunnerStatusReq,
) (*strategypb.SetRunnerStatusRsp, error) {
	if req == nil || req.GetRunnerId() == "" || s.Repo == nil {
		return &strategypb.SetRunnerStatusRsp{
			RetInfo: invalid(errors.New("runner_id and status are required")),
		}, nil
	}
	status := domain.RunnerStatus(req.GetStatus())
	if status != domain.RunnerStatusEnabled && status != domain.RunnerStatusDisabled {
		return &strategypb.SetRunnerStatusRsp{
			RetInfo: invalid(errors.New("runner status must be ENABLED or DISABLED")),
		}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	if runner.Status == status {
		var reconcileErr error
		if status == domain.RunnerStatusEnabled {
			reconcileErr = s.claimRunner(ctx, runner)
		} else {
			reconcileErr = s.releaseRunner(ctx, runner)
		}
		if reconcileErr != nil {
			return &strategypb.SetRunnerStatusRsp{
				RetInfo: invalid(reconcileErr),
			}, nil
		}
		return &strategypb.SetRunnerStatusRsp{
			RetInfo: success(), Runner: runnerProto(runner),
		}, nil
	}
	if status == domain.RunnerStatusEnabled {
		if err := s.claimRunner(ctx, runner); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
		if err := s.Repo.SetRunnerStatus(ctx, runner.ID, status, s.now()); err != nil {
			s.releaseRunner(ctx, runner)
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
	} else {
		if err := s.Repo.SetRunnerStatus(ctx, runner.ID, status, s.now()); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
		if err := s.releaseRunner(ctx, runner); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
	}
	updated, err := s.Repo.GetRunner(ctx, runner.ID)
	if err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.SetRunnerStatusRsp{RetInfo: success(), Runner: runnerProto(updated)}, nil
}

func (s *Service) ListStrategyResults(
	ctx context.Context,
	req *strategypb.ListStrategyResultsReq,
) (*strategypb.ListStrategyResultsRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListStrategyResultsRsp{
			RetInfo: invalid(errors.New("strategy repository is unavailable")),
		}, nil
	}
	filter := store.ResultFilter{}
	if req != nil {
		filter.RunnerID = req.GetRunnerId()
	}
	if filter.RunnerID != "" {
		runner, err := s.Repo.GetRunner(ctx, filter.RunnerID)
		if err != nil {
			return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
		}
		if err := ensureRunnerScope(ctx, runner); err != nil {
			return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
		}
	} else if requestSpaceID(ctx) != "" {
		return &strategypb.ListStrategyResultsRsp{
			RetInfo: invalid(errors.New("runner_id is required for a scoped result query")),
		}, nil
	}
	values, err := s.Repo.ListResults(ctx, filter)
	if err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.StrategyResult, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, resultProto(value))
	}
	return &strategypb.ListStrategyResultsRsp{
		RetInfo: success(), Results: items, Total: int64(len(values)),
		Page: int32(page), PageSize: int32(size),
	}, nil
}

func (s *Service) GetStrategyResult(
	ctx context.Context,
	req *strategypb.GetStrategyResultReq,
) (*strategypb.GetStrategyResultRsp, error) {
	if req == nil || req.GetResultId() == "" || s.Repo == nil {
		return &strategypb.GetStrategyResultRsp{
			RetInfo: invalid(errors.New("result_id is required")),
		}, nil
	}
	value, err := s.Repo.GetResult(ctx, req.GetResultId())
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, value.RunnerID)
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyResultRsp{
		RetInfo: success(), Result: resultProto(value),
	}, nil
}

func (s *Service) ListStrategyTargets(
	ctx context.Context,
	req *strategypb.ListStrategyTargetsReq,
) (*strategypb.ListStrategyTargetsRsp, error) {
	if req == nil || req.GetRunnerId() == "" || s.Repo == nil {
		return &strategypb.ListStrategyTargetsRsp{
			RetInfo: invalid(errors.New("runner_id is required")),
		}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	targets, err := decodeTargets(runner.CurrentTargetsJSON)
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.ListStrategyTargetsRsp{
		RetInfo: success(), Targets: targets, CommandSequence: runner.CommandSequence,
	}, nil
}

func (s *Service) claimRunner(ctx context.Context, runner domain.StrategyRunner) error {
	if runner.LogicalAccountID == nil {
		return nil
	}
	if s.LogicalAccounts == nil {
		return errors.New("logical account owner client is unavailable")
	}
	return s.LogicalAccounts.Claim(ctx, runner.SpaceID, *runner.LogicalAccountID, runner.ID)
}

func (s *Service) releaseRunner(ctx context.Context, runner domain.StrategyRunner) error {
	if runner.LogicalAccountID == nil {
		return nil
	}
	if s.LogicalAccounts == nil {
		return errors.New("logical account owner client is unavailable")
	}
	return s.LogicalAccounts.Release(ctx, runner.SpaceID, *runner.LogicalAccountID, runner.ID)
}

func requestSpaceID(ctx context.Context) string {
	for _, key := range []string{"space_id", "X-Space-Id", "x-space-id"} {
		if value := string(trpc.GetMetaData(ctx, key)); value != "" {
			return value
		}
	}
	return ""
}

func ensureRunnerScope(ctx context.Context, runner domain.StrategyRunner) error {
	if scoped := requestSpaceID(ctx); scoped != "" && scoped != runner.SpaceID {
		return fmt.Errorf("runner %q is outside the current space", runner.ID)
	}
	return nil
}

func strategyProto(value domain.Strategy) *strategypb.Strategy {
	return &strategypb.Strategy{
		StrategyId: value.ID, Name: value.Name, ManifestYaml: value.ManifestYAML,
		SourceCode: value.SourceCode, SourceHash: value.SourceHash,
		CreatedAt: formatTime(value.CreatedAt),
	}
}

func runnerProto(value domain.StrategyRunner) *strategypb.StrategyRunner {
	targets, _ := decodeTargets(value.CurrentTargetsJSON)
	return &strategypb.StrategyRunner{
		RunnerId: value.ID, StrategyId: value.StrategyID, SpaceId: value.SpaceID,
		ViewId: value.ViewID, Frequency: value.Frequency, ParamsJson: string(value.ParamsJSON),
		LogicalAccountId: dereference(value.LogicalAccountID), Status: string(value.Status),
		CurrentTargets: targets, CommandSequence: value.CommandSequence,
		LastResultId:  dereference(value.LastResultID),
		LastSuccessAt: formatOptionalTime(value.LastSuccessAt),
		LastError:     dereference(value.LastError), CreatedAt: formatTime(value.CreatedAt),
		UpdatedAt: formatTime(value.UpdatedAt),
	}
}

func resultProto(value domain.StrategyResult) *strategypb.StrategyResult {
	return &strategypb.StrategyResult{
		ResultId: value.ID, RunnerId: value.RunnerID, StrategyId: value.StrategyID,
		TriggerBarTime: formatTime(value.TriggerBarTime), Namespace: value.Namespace,
		InputHash: value.InputHash, Action: string(value.Action),
		OutputJson: string(value.OutputJSON), CommandSequence: value.CommandSequence,
		CreatedAt: formatTime(value.CreatedAt),
	}
}

func decodeTargets(raw json.RawMessage) ([]*strategypb.InstrumentTarget, error) {
	var targets []domain.InstrumentTarget
	if len(raw) == 0 {
		targets = []domain.InstrumentTarget{}
	} else if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, err
	}
	result := make([]*strategypb.InstrumentTarget, 0, len(targets))
	for _, target := range targets {
		result = append(result, &strategypb.InstrumentTarget{
			InstrumentId: target.InstrumentID, Quantity: target.Quantity,
		})
	}
	return result, nil
}

func pageBounds(value *strategypb.PageRequest, total int) (int, int, int, int) {
	page, size := 1, 20
	if value != nil {
		if value.GetPage() > 0 {
			page = int(value.GetPage())
		}
		if value.GetPageSize() > 0 {
			size = int(value.GetPageSize())
		}
	}
	if size > 200 {
		size = 200
	}
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := min(start+size, total)
	return page, size, start, end
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
