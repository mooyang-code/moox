package rpc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// ValidateStrategy 解析校验 DSL；给出 view_id 时解析绑定并在最新周期试算一次，结果不落库。
func (s *Service) ValidateStrategy(ctx context.Context, req *strategypb.ValidateStrategyReq) (*strategypb.ValidateStrategyRsp, error) {
	if req == nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(errNilRequest)}, nil
	}
	strategy, _, err := parseDefinition(req.GetDslYaml())
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{err.Error()}}, nil
	}
	rsp := &strategypb.ValidateStrategyRsp{RetInfo: success(), Diagnostics: []string{}}
	if strings.TrimSpace(req.GetViewId()) == "" {
		return rsp, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if s.Resolver == nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(errDependenciesUnavailable), Diagnostics: []string{errDependenciesUnavailable.Error()}}, nil
	}
	resolved, program, err := s.Resolver.Resolve(ctx, scoped, req.GetViewId(), strategy)
	if err != nil {
		return &strategypb.ValidateStrategyRsp{RetInfo: invalid(err), Diagnostics: []string{err.Error()}}, nil
	}
	resolvedJSON, _ := json.Marshal(resolved)
	rsp.ResolvedJson = string(resolvedJSON)
	loaded, err := s.Resolver.LoadLatest(ctx, scoped, resolved, program, s.nowTime())
	if err != nil {
		rsp.Diagnostics = append(rsp.Diagnostics, "试算未完成："+err.Error())
		return rsp, nil
	}
	decision, err := engine.Evaluate(program, loaded.Frame, engine.State{})
	if err != nil {
		rsp.Diagnostics = append(rsp.Diagnostics, "试算失败："+err.Error())
		return rsp, nil
	}
	rsp.Trial = trialProto(decision, loaded, req.GetViewId())
	rsp.TrialItems = itemProtos(trigger.ResultItems(decision.Items))
	for _, note := range loaded.Sets.Notes {
		rsp.Diagnostics = append(rsp.Diagnostics, note)
	}
	for _, note := range decision.Summary.Notes {
		rsp.Diagnostics = append(rsp.Diagnostics, note)
	}
	return rsp, nil
}

func trialProto(decision engine.Decision, loaded input.Loaded, viewID string) *strategypb.StrategyResult {
	_, targetsJSON, _ := trigger.EncodeTargets(decision.Targets)
	states, _ := json.Marshal(decision.State)
	summary, _ := trigger.EncodeSummary(decision.Summary, loaded.Sets.Notes)
	inputJSON, _ := json.Marshal(store.InputRecord{ViewID: viewID, BarStart: loaded.Boundary.StorageStart})
	result := store.Result{BarEndTime: loaded.Boundary.BarEnd, Status: decision.Status, SkipReason: decision.SkipReason, TargetsJSON: targetsJSON, RuleStatesJSON: states, SummaryJSON: summary, InputJSON: inputJSON, PublishStatus: store.PublishNone}
	return resultProto(result)
}
