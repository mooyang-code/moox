package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"time"
)

type Service struct {
	Repo                 *store.Store
	Registry             *registry.Service
	Workers              int
	ReadyWorkers         int
	Engine               *engine.Engine
	LiveExecutionEnabled bool
}

func (s *Service) CreateStrategy(ctx context.Context, req *strategypb.CreateStrategyReq) (*strategypb.CreateStrategyRsp, error) {
	if req == nil || req.GetStrategy() == nil || s.Registry == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(fmt.Errorf("strategy is required"))}, nil
	}
	if s.Engine == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(errors.New("strategy runtime is unavailable; draft-only publish is not supported by this service"))}, nil
	}
	p := req.GetStrategy()
	d, err := s.Registry.Prepare(p.GetManifestYaml(), p.GetSourceCode())
	if err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	// Validate/import the source before acknowledging it. The definition is
	// already immutable in the repository, so a failed load can never silently
	// replace a previously accepted version.
	if err := s.Engine.Load(ctx, d); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(fmt.Errorf("load strategy source: %w", err))}, nil
	}
	// A source becomes runnable only after a worker has imported it.
	d.Status = "enabled"
	if err := s.Registry.Save(ctx, d); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.CreateStrategyRsp{RetInfo: success(), Strategy: &strategypb.StrategyDef{StrategyId: d.StrategyID, Version: d.Version, ApiVersion: d.API, ManifestYaml: d.ManifestYAML, SourceCode: d.SourceCode, SourceHash: d.SourceHash, Status: d.Status}}, nil
}
func (s *Service) RunOnce(ctx context.Context, req *strategypb.RunOnceReq) (*strategypb.RunOnceRsp, error) {
	if req == nil || req.GetBindingId() == "" || req.GetTriggerBarTime() == "" {
		return &strategypb.RunOnceRsp{RetInfo: invalid(errors.New("binding_id and trigger_bar_time are required"))}, nil
	}
	if req.GetDataJson() == "" || req.GetDataRevision() == "" {
		return &strategypb.RunOnceRsp{RetInfo: invalid(errors.New("data_json and data_revision are required for point-in-time run"))}, nil
	}
	if s.Repo == nil || s.Engine == nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(errors.New("strategy runtime is unavailable"))}, nil
	}
	binding, err := s.Repo.GetBinding(ctx, req.GetBindingId())
	if err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("get binding: %w", err))}, nil
	}
	if binding.Status != "enabled" {
		return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("binding %q is not enabled", binding.BindingID))}, nil
	}
	d, err := s.Repo.GetDefinition(ctx, binding.StrategyID, binding.StrategyVersion)
	if err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("get strategy: %w", err))}, nil
	}
	if d.Status != "enabled" {
		return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("strategy %q@%s is not enabled", d.StrategyID, d.Version))}, nil
	}
	state, err := s.Repo.GetState(ctx, binding.BindingID)
	if err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("get state: %w", err))}, nil
	}
	params := map[string]any{}
	if binding.ParamsJSON != "" {
		if err := json.Unmarshal([]byte(binding.ParamsJSON), &params); err != nil {
			return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("decode binding params: %w", err))}, nil
		}
	}
	var data []map[string]any
	if req.GetDataJson() != "" {
		if err := json.Unmarshal([]byte(req.GetDataJson()), &data); err != nil {
			return &strategypb.RunOnceRsp{RetInfo: invalid(fmt.Errorf("decode snapshot data: %w", err))}, nil
		}
	}
	namespace := req.GetNamespace()
	if namespace == "" {
		namespace = "default"
	}
	runID := newRunID()
	task := domain.Task{RunID: runID, BindingID: binding.BindingID, StrategyID: binding.StrategyID, Version: binding.StrategyVersion, SpaceID: binding.SpaceID, Freq: binding.Freq, TriggerBarTime: req.GetTriggerBarTime(), Namespace: namespace, DataRevision: req.GetDataRevision(), PreviousState: state, Params: params, Data: data}
	out, inputHash, err := (&action.Service{Repo: s.Repo, Engine: s.Engine}).Evaluate(ctx, task, d)
	if err != nil {
		return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
	}
	if req.GetCommit() {
		if err := (&action.Service{Repo: s.Repo, Engine: s.Engine}).Commit(ctx, task, out, inputHash); err != nil {
			_ = s.Repo.UpsertHealth(ctx, domain.BindingHealth{BindingID: binding.BindingID, Status: "failed", Mode: "observe", LastRunID: runID, LastErrorType: "commit", LastErrorMessage: err.Error(), LastDataRevision: task.DataRevision, ObservedAt: time.Now().UTC()})
			return &strategypb.RunOnceRsp{RetInfo: invalid(err)}, nil
		}
	}
	_ = s.Repo.UpsertHealth(ctx, domain.BindingHealth{BindingID: binding.BindingID, Status: "running", Mode: "observe", LastRunID: runID, LastSuccessAt: time.Now().UTC(), LastDataRevision: task.DataRevision, WorkerStatus: "ready", ObservedAt: time.Now().UTC()})
	raw, _ := json.Marshal(out)
	return &strategypb.RunOnceRsp{RetInfo: success(), Run: &strategypb.StrategyRun{RunId: runID, BindingId: binding.BindingID, TriggerBarTime: req.GetTriggerBarTime(), Action: out.Action, Status: map[bool]string{true: "accepted", false: "observed"}[req.GetCommit()], OutputJson: string(raw)}}, nil
}
func (s *Service) GetEngineStatus(context.Context, *strategypb.GetEngineStatusReq) (*strategypb.GetEngineStatusRsp, error) {
	return &strategypb.GetEngineStatusRsp{RetInfo: success(), Workers: int32(s.Workers), ReadyWorkers: int32(s.ReadyWorkers), LiveExecutionEnabled: s.LiveExecutionEnabled}, nil
}
func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}
func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}

func newRunID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return "run-fallback"
}

var _ = domain.ActionHold
