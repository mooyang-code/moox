package rpc

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

var (
	errNilRequest              = errors.New("请求不能为空")
	errDependenciesUnavailable = errors.New("Storage 与 Factor 依赖未配置")
)

func definitionProto(value store.Definition) *strategypb.Strategy {
	return &strategypb.Strategy{StrategyId: value.StrategyID, Name: value.Name, DslYaml: value.DSLYaml, DslHash: value.DSLHash, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
}

func instanceProto(value store.Instance) *strategypb.StrategyInstance {
	return &strategypb.StrategyInstance{
		InstanceId: value.InstanceID, StrategyId: value.StrategyID, SpaceId: value.SpaceID, ViewId: value.ViewID,
		LogicalAccountId: dereference(value.LogicalAccountID), Enabled: value.Enabled, SessionId: dereference(value.SessionID),
		ResolvedJson: string(value.ResolvedJSON), Health: value.Health, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
	}
}

func resultProto(value store.Result) *strategypb.StrategyResult {
	return &strategypb.StrategyResult{
		ResultId: value.ResultID, InstanceId: value.InstanceID, SessionId: value.SessionID,
		BarEndTime: formatTime(value.BarEndTime), ValidUntil: formatTime(value.ValidUntil),
		Status: value.Status, SkipReason: value.SkipReason, DslHash: value.DSLHash,
		Targets: targetProtos(value.TargetsJSON), SummaryJson: string(value.SummaryJSON), InputJson: string(value.InputJSON),
		RuleStatesJson: string(value.RuleStatesJSON), PublishStatus: string(value.PublishStatus), CreatedAt: formatTime(value.CreatedAt),
	}
}

func targetProtos(raw json.RawMessage) []*strategypb.InstrumentTarget {
	targets, err := trigger.DecodeTargets(raw)
	if err != nil {
		return []*strategypb.InstrumentTarget{}
	}
	out := make([]*strategypb.InstrumentTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, &strategypb.InstrumentTarget{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight})
	}
	return out
}

func itemProtos(items []store.ResultItem) []*strategypb.StrategyResultItem {
	out := make([]*strategypb.StrategyResultItem, 0, len(items))
	for _, item := range items {
		row := &strategypb.StrategyResultItem{RuleId: item.RuleID, InstrumentId: item.InstrumentID, Stage: item.Stage, Reason: item.Reason, Weight: dereference(item.Weight)}
		if item.Score != nil {
			row.Score = strconv.FormatFloat(*item.Score, 'g', -1, 64)
		}
		if item.Rank != nil {
			row.Rank = int32(*item.Rank)
		}
		out = append(out, row)
	}
	return out
}

func replayProto(value store.Replay) *strategypb.Replay {
	replay := &strategypb.Replay{
		ReplayId: value.ReplayID, StrategyId: dereference(value.StrategyID), DslYaml: value.DSLYaml, SpaceId: value.SpaceID, ViewId: value.ViewID,
		StartTime: formatTime(value.StartTime), EndTime: formatTime(value.EndTime), FeeBps: value.FeeBps, Status: value.Status,
		MetricsJson: string(value.MetricsJSON), Error: value.Error, CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt),
		DslHash: value.DSLHash, InstanceId: dereference(value.InstanceID), SessionId: dereference(value.SessionID), Calendar: value.Calendar,
	}
	if value.ProgressTime != nil {
		replay.ProgressTime = formatTime(*value.ProgressTime)
	}
	return replay
}

func replayBarProto(value store.ReplayBar) *strategypb.ReplayBar {
	return &strategypb.ReplayBar{
		BarEndTime: formatTime(value.BarEndTime), Status: value.Status, Targets: targetProtos(value.TargetsJSON), PositionsJson: string(value.PositionsJSON), SummaryJson: string(value.SummaryJSON),
		BarReturn: value.Return, Equity: value.Equity, Turnover: value.Turnover, Fee: value.Fee,
		Holdings: int32(value.Holdings), Frozen: int32(value.Frozen), SkipReason: value.SkipReason, Unfilled: int32(value.Unfilled), Liquidated: int32(value.Liquidated),
	}
}

// pageValues 返回页码与页大小（默认 1 / 20，上限 500）。
func pageValues(value *strategypb.PageRequest) (int, int) {
	page, size := 1, 20
	if value != nil {
		if value.GetPage() > 0 {
			page = int(value.GetPage())
		}
		if value.GetPageSize() > 0 {
			size = int(value.GetPageSize())
		}
	}
	if size > 500 {
		size = 500
	}
	return page, size
}

// pageBounds 对内存中的列表分页。
func pageBounds(value *strategypb.PageRequest, total int) (int, int, int, int) {
	page, size := pageValues(value)
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

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
