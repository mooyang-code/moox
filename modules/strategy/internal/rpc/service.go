package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	strategyaction "github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type StrategyRuntime interface {
	Load(context.Context, domain.Strategy) error
	Run(
		context.Context,
		domain.ExecutionRequest,
		domain.Strategy,
	) (domain.Output, string, error)
}

type LogicalAccountOwner interface {
	Validate(context.Context, string, string) error
	Claim(context.Context, string, string, string) error
	Release(context.Context, string, string, string) error
}

type ResultActions interface {
	Commit(
		context.Context,
		domain.StrategyResult,
		domain.Output,
	) (store.CommitResultOutcome, error)
	RecordFailure(context.Context, string, error, time.Time) error
}

type Service struct {
	Repo            *store.Store
	Registry        *registry.Service
	Runtime         StrategyRuntime
	Results         ResultActions
	LogicalAccounts LogicalAccountOwner
	Workers         int
	ReadyWorkers    int
	Now             func() time.Time
	NewID           func() string
}

func (s *Service) CreateStrategy(
	ctx context.Context,
	req *strategypb.CreateStrategyReq,
) (*strategypb.CreateStrategyRsp, error) {
	if req == nil || req.GetStrategy() == nil || s.Registry == nil || s.Runtime == nil {
		return &strategypb.CreateStrategyRsp{
			RetInfo: invalid(errors.New("strategy registry and runtime are required")),
		}, nil
	}
	value := req.GetStrategy()
	strategy, err := s.Registry.Prepare(
		value.GetStrategyId(),
		value.GetName(),
		value.GetManifestYaml(),
		value.GetSourceCode(),
	)
	if err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.Runtime.Load(ctx, strategy); err != nil {
		return &strategypb.CreateStrategyRsp{
			RetInfo: invalid(fmt.Errorf("load strategy source: %w", err)),
		}, nil
	}
	if err := s.Registry.Save(ctx, strategy); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.CreateStrategyRsp{
		RetInfo:  success(),
		Strategy: strategyProto(strategy),
	}, nil
}

func (s *Service) CreateRunner(
	ctx context.Context,
	req *strategypb.CreateRunnerReq,
) (*strategypb.CreateRunnerRsp, error) {
	if req == nil || req.GetRunner() == nil || s.Repo == nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(errors.New("runner is required"))}, nil
	}
	value := req.GetRunner()
	if err := validateRunnerInput(value); err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if scoped := requestSpaceID(ctx); scoped != "" && scoped != value.GetSpaceId() {
		return &strategypb.CreateRunnerRsp{
			RetInfo: invalid(errors.New("runner is outside the current space")),
		}, nil
	}
	if _, err := s.Repo.GetStrategy(ctx, value.GetStrategyId()); err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(fmt.Errorf("get strategy: %w", err))}, nil
	}
	if value.GetLogicalAccountId() != "" {
		if s.LogicalAccounts == nil {
			return &strategypb.CreateRunnerRsp{
				RetInfo: invalid(errors.New("logical account owner client is unavailable")),
			}, nil
		}
		if err := s.LogicalAccounts.Validate(
			ctx,
			value.GetSpaceId(),
			value.GetLogicalAccountId(),
		); err != nil {
			return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	now := s.now()
	runner := domain.StrategyRunner{
		ID: value.GetRunnerId(), StrategyID: value.GetStrategyId(),
		SpaceID: value.GetSpaceId(), ViewID: value.GetViewId(),
		Frequency: value.GetFrequency(), ParamsJSON: json.RawMessage(value.GetParamsJson()),
		LogicalAccountID: optionalString(value.GetLogicalAccountId()),
		Status:           domain.RunnerStatusDisabled, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Repo.CreateRunner(ctx, runner); err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	created, err := s.Repo.GetRunner(ctx, runner.ID)
	if err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.CreateRunnerRsp{RetInfo: success(), Runner: runnerProto(created)}, nil
}

func (s *Service) RunOnce(
	ctx context.Context,
	req *strategypb.RunOnceReq,
) (*strategypb.RunOnceRsp, error) {
	if req == nil || strings.TrimSpace(req.GetRunnerId()) == "" ||
		strings.TrimSpace(req.GetTriggerBarTime()) == "" ||
		strings.TrimSpace(req.GetDataJson()) == "" {
		return &strategypb.RunOnceRsp{
			RetInfo: invalid(errors.New("runner_id, trigger_bar_time and data_json are required")),
		}, nil
	}
	if s.Repo == nil || s.Runtime == nil {
		return &strategypb.RunOnceRsp{
			RetInfo: invalid(errors.New("strategy runtime is unavailable")),
		}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	strategy, err := s.Repo.GetStrategy(ctx, runner.StrategyID)
	if err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	trigger, err := time.Parse(time.RFC3339Nano, req.GetTriggerBarTime())
	if err != nil {
		return &strategypb.RunOnceRsp{
			RetInfo: invalid(fmt.Errorf("trigger_bar_time must be RFC3339: %w", err)),
		}, nil
	}
	var data []map[string]any
	if err := json.Unmarshal([]byte(req.GetDataJson()), &data); err != nil {
		return &strategypb.RunOnceRsp{
			RetInfo: invalid(fmt.Errorf("decode complete history: %w", err)),
		}, nil
	}
	var params map[string]any
	if err := json.Unmarshal(runner.ParamsJSON, &params); err != nil {
		return &strategypb.RunOnceRsp{
			RetInfo: invalid(fmt.Errorf("decode runner params: %w", err)),
		}, nil
	}
	namespace := strings.TrimSpace(req.GetNamespace())
	if namespace == "" {
		namespace = "default"
	}
	resultID := s.newID()
	execution := domain.ExecutionRequest{
		RequestID: resultID, StrategyID: strategy.ID, RunnerID: runner.ID,
		TriggerBarTime: trigger.UTC().Format(time.RFC3339Nano),
		Namespace:      namespace, Params: params, Data: data,
	}
	if err := s.Runtime.Load(ctx, strategy); err != nil {
		s.recordFailure(ctx, runner.ID, err)
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	output, inputHash, err := s.Runtime.Run(ctx, execution, strategy)
	if err != nil {
		s.recordFailure(ctx, runner.ID, err)
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	result := domain.StrategyResult{
		ID: resultID, RunnerID: runner.ID, StrategyID: strategy.ID,
		TriggerBarTime: trigger.UTC(), Namespace: namespace, InputHash: inputHash,
		Action: output.Action, CreatedAt: s.now(),
	}
	if runner.Status != domain.RunnerStatusEnabled {
		result.OutputJSON, err = previewOutputJSON(output)
		if err != nil {
			return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
		}
		return &strategypb.RunOnceRsp{
			RetInfo: success(), Result: resultProto(result), Accepted: false,
		}, nil
	}
	if s.Results == nil {
		return &strategypb.RunOnceRsp{
			RetInfo: invalid(errors.New("strategy result service is unavailable")),
		}, nil
	}
	outcome, err := s.Results.Commit(ctx, result, output)
	if err != nil {
		s.recordFailure(ctx, runner.ID, err)
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.RunOnceRsp{
		RetInfo: success(), Result: resultProto(outcome.Result), Accepted: true,
	}, nil
}

func (s *Service) GetEngineStatus(
	context.Context,
	*strategypb.GetEngineStatusReq,
) (*strategypb.GetEngineStatusRsp, error) {
	return &strategypb.GetEngineStatusRsp{
		RetInfo: success(), Workers: int32(s.Workers), ReadyWorkers: int32(s.ReadyWorkers),
	}, nil
}

func (s *Service) recordFailure(ctx context.Context, runnerID string, failure error) {
	if s.Results != nil {
		_ = s.Results.RecordFailure(ctx, runnerID, failure, s.now())
	}
}

var _ ResultActions = (*strategyaction.Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	value := make([]byte, 12)
	if _, err := rand.Read(value); err == nil {
		return hex.EncodeToString(value)
	}
	return fmt.Sprintf("result-%d", s.now().UnixNano())
}

func validateRunnerInput(value *strategypb.StrategyRunner) error {
	if strings.TrimSpace(value.GetRunnerId()) == "" ||
		strings.TrimSpace(value.GetStrategyId()) == "" ||
		strings.TrimSpace(value.GetSpaceId()) == "" ||
		strings.TrimSpace(value.GetViewId()) == "" ||
		strings.TrimSpace(value.GetFrequency()) == "" {
		return errors.New("runner identity, strategy, space, view and frequency are required")
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(value.GetParamsJson()), &params); err != nil {
		return fmt.Errorf("runner params_json must be a JSON object: %w", err)
	}
	return nil
}

func previewOutputJSON(output domain.Output) (json.RawMessage, error) {
	targets := output.Targets
	if targets == nil {
		targets = []domain.InstrumentTarget{}
	}
	return json.Marshal(struct {
		Targets   []domain.InstrumentTarget `json:"targets"`
		DebugInfo map[string]any            `json:"debug_info,omitempty"`
	}{
		Targets: targets, DebugInfo: output.DebugInfo,
	})
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}
