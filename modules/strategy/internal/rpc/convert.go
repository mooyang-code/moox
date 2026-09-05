package rpc

import (
	"encoding/json"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

func strategyProto(value domain.Strategy) *strategypb.Strategy {
	return &strategypb.Strategy{StrategyId: value.ID, Name: value.Name, StrategyName: value.Name, Kind: value.Kind, ManifestYaml: value.ManifestYAML, DslYaml: value.ManifestYAML, CompiledJson: string(value.CompiledJSON), SourceHash: value.SourceHash, CreatedAt: formatTime(value.CreatedAt)}
}

func strategyDefinitionProto(value store.StrategyDefinition) *strategypb.Strategy {
	return &strategypb.Strategy{StrategyId: value.StrategyID, Name: value.StrategyName, StrategyName: value.StrategyName, DslYaml: value.DSLYaml, ManifestYaml: value.DSLYaml, CreatedAt: formatTime(value.CreatedAt)}
}

func instanceProto(value store.StrategyInstance) *strategypb.StrategyInstance {
	return &strategypb.StrategyInstance{InstanceId: value.InstanceID, StrategyId: value.StrategyID, SpaceId: value.SpaceID, InputBindingsJson: string(value.InputBindingsJSON), LogicalAccountId: dereference(value.LogicalAccountID), Enabled: value.Enabled, SessionId: dereference(value.SessionID), CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}

func runnerProto(value domain.StrategyRunner) *strategypb.StrategyRunner {
	targets, _ := decodeTargets(value.CurrentTargetsJSON)
	return &strategypb.StrategyRunner{RunnerId: value.ID, StrategyId: value.StrategyID, SpaceId: value.SpaceID, ViewId: value.SourceViewID, SourceViewId: value.SourceViewID, Frequency: value.Frequency, LogicalAccountId: dereference(value.LogicalAccountID), Status: string(value.Status), CurrentTargets: targets, CommandSequence: value.CommandSequence, LastResultId: dereference(value.LastResultID), LastSuccessAt: formatOptionalTime(value.LastSuccessAt), LastError: dereference(value.LastError), CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}

func resultProto(value domain.StrategyResult) *strategypb.StrategyResult {
	period := formatTime(value.PeriodTime)
	return &strategypb.StrategyResult{
		ResultId: value.ID, RunnerId: value.RunnerID, StrategyId: value.StrategyID,
		TriggerBarTime: period, InputHash: value.InputHash, Action: string(value.Action),
		OutputJson: string(value.TargetsJSON), CommandSequence: value.CommandSequence, CreatedAt: formatTime(value.CreatedAt),
		PeriodTime: period, Targets: decodeTargetProto(value.TargetsJSON), DebugInfoJson: string(value.DebugInfoJSON),
	}
}

func modernResultProto(value store.StrategyResult) *strategypb.StrategyResult {
	return &strategypb.StrategyResult{
		ResultId: value.ResultID, InstanceId: value.InstanceID, SessionId: value.SessionID,
		PeriodTime: formatTime(value.BarEndTime), TriggerBarTime: formatTime(value.BarEndTime),
		ValidUntil: formatTime(value.ValidUntil), Targets: decodeTargetProto(value.TargetsJSON),
		OutputJson: string(value.TargetsJSON),
		RuleStatesJson: string(value.RuleStatesJSON), PublishStatus: string(value.PublishStatus),
		CreatedAt: formatTime(value.CreatedAt),
	}
}

func decodeTargets(raw json.RawMessage) ([]*strategypb.InstrumentTarget, error) {
	if len(raw) == 0 {
		return []*strategypb.InstrumentTarget{}, nil
	}
	var targets []domain.InstrumentTarget
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, err
	}
	result := make([]*strategypb.InstrumentTarget, 0, len(targets))
	for _, target := range targets {
		result = append(result, &strategypb.InstrumentTarget{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight})
	}
	return result, nil
}

func decodeTargetProto(raw json.RawMessage) []*strategypb.InstrumentTarget {
	targets, err := decodeTargets(raw)
	if err != nil {
		return []*strategypb.InstrumentTarget{}
	}
	return targets
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
