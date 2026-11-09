package rpc

import (
	"context"
	"encoding/json"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// ValidateStrategy 解析校验 DSL；给出 factor_tasks 时解析绑定并在最新周期试算一次，结果不落库。
func (s *Service) ValidateStrategy(ctx context.Context, req *strategypb.ValidateStrategyReq) (*strategypb.ValidateStrategyRsp, error) {
	if req == nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(errNilRequest)}, nil
	}
	strategy, _, err := parseDefinition(req.GetDslYaml())
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{publicMessage(err)}}, nil
	}
	rsp := &strategypb.ValidateStrategyRsp{RetInfo: success(), Diagnostics: []string{}}
	members, err := s.checkPool(ctx, strategy)
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{publicMessage(err)}}, nil
	}
	if len(req.GetFactorTasks()) == 0 {
		return rsp, nil
	}
	tasks, err := normalizeFactorTasks(req.GetFactorTasks())
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{err.Error()}}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if s.Resolver == nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(errDependenciesUnavailable), Diagnostics: []string{errDependenciesUnavailable.Error()}}, nil
	}
	if strategy.IsPool() {
		return s.validatePool(ctx, rsp, scoped, tasks, strategy, members), nil
	}
	resolved, program, err := s.Resolver.Resolve(ctx, scoped, tasks, strategy)
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{err.Error()}}, nil
	}
	if err := input.CheckResolvedMarkets(resolved, tasks); err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{err.Error()}}, nil
	}
	resolvedJSON, _ := json.Marshal(resolved)
	rsp.ResolvedJson = string(resolvedJSON)
	loaded, err := s.Resolver.LoadLatest(ctx, scoped, resolved, program, s.nowTime())
	if err != nil {
		rsp.Diagnostics = append(rsp.Diagnostics, "试算未完成："+publicMessage(err))
		return rsp, nil
	}
	decision, err := engine.Evaluate(program, loaded.Frame, engine.State{})
	if err != nil {
		rsp.Diagnostics = append(rsp.Diagnostics, "试算失败："+err.Error())
		return rsp, nil
	}
	rsp.Trial = trialProto(decision, loaded, resolved.ViewIDs())
	rsp.TrialItems = itemProtos(trigger.ResultItems(decision.Items))
	for _, note := range loaded.Sets.Notes {
		rsp.Diagnostics = append(rsp.Diagnostics, note)
	}
	for _, note := range decision.Summary.Notes {
		rsp.Diagnostics = append(rsp.Diagnostics, note)
	}
	return rsp, nil
}

func trialProto(decision engine.Decision, loaded input.Loaded, viewIDs []string) *strategypb.StrategyResult {
	_, targetsJSON, _ := trigger.EncodeTargets(decision.Targets)
	states, _ := json.Marshal(decision.State)
	summary, _ := trigger.EncodeSummary(decision.Summary, loaded.Sets.Notes)
	views := make(map[string]store.ViewInput, len(viewIDs))
	for _, viewID := range viewIDs {
		views[viewID] = store.ViewInput{}
	}
	inputJSON, _ := json.Marshal(store.InputRecord{Views: views, BarStart: loaded.Boundary.StorageStart})
	result := store.Result{BarEndTime: loaded.Boundary.BarEnd, Status: decision.Status, SkipReason: decision.SkipReason, TargetsJSON: targetsJSON, RuleStatesJSON: states, SummaryJSON: summary, InputJSON: inputJSON, PublishStatus: store.PublishNone}
	return resultProto(result)
}

// validatePool 解析策略池的绑定并校验各成员；策略池的试算在池实例启用后的预热与运行中查看（成员的净值与分配比例依赖历史）。
func (s *Service) validatePool(ctx context.Context, rsp *strategypb.ValidateStrategyRsp, spaceID string, tasks map[string]string, strategy dsl.Strategy, members []input.PoolMemberDef) *strategypb.ValidateStrategyRsp {
	pooled, err := s.Resolver.ResolvePool(ctx, spaceID, input.TaskIDs(tasks), strategy, members)
	if err == nil {
		err = input.CheckResolvedMarkets(pooled.Resolved, tasks)
	}
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{publicMessage(err)}}
	}
	resolvedJSON, _ := json.Marshal(pooled.Resolved)
	rsp.ResolvedJson = string(resolvedJSON)
	rsp.Diagnostics = append(rsp.Diagnostics, "策略池不做试算：成员的净值与分配比例依赖历史，启用后先预热再运行")
	return rsp
}
