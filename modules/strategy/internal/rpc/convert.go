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
	return &strategypb.Strategy{StrategyId: value.StrategyID, Name: value.Name, DslYaml: value.DSLYaml, DslHash: value.DSLHash, CreatedAt: millis(value.CreatedAt), UpdatedAt: millis(value.UpdatedAt)}
}

func instanceProto(value store.Instance) *strategypb.StrategyInstance {
	return &strategypb.StrategyInstance{
		InstanceId: value.InstanceID, StrategyId: value.StrategyID, SpaceId: value.SpaceID, FactorTasks: value.FactorTasks,
		LogicalAccountId: dereference(value.LogicalAccountID), Enabled: value.Enabled, SessionId: dereference(value.SessionID),
		ResolvedJson: string(value.ResolvedJSON), Health: value.Health, CreatedAt: millis(value.CreatedAt), UpdatedAt: millis(value.UpdatedAt),
		ConfirmedAt: optionalMillis(value.ConfirmedAt),
	}
}

func resultProto(value store.Result) *strategypb.StrategyResult {
	return &strategypb.StrategyResult{
		ResultId: value.ResultID, InstanceId: value.InstanceID, SessionId: value.SessionID,
		BarEndTime: millis(value.BarEndTime), ValidUntil: millis(value.ValidUntil),
		Status: value.Status, SkipReason: value.SkipReason, DslHash: value.DSLHash,
		Targets: targetProtos(value.TargetsJSON), SummaryJson: string(value.SummaryJSON), InputJson: string(value.InputJSON),
		PublishStatus: string(value.PublishStatus), CreatedAt: millis(value.CreatedAt),
		NextParamsJson: string(value.NextParamsJSON), ParamsError: value.ParamsError, ParamsDeliveredAt: optionalMillis(value.ParamsDeliveredAt),
	}
}

// optionalMillis 把可选时刻写成 Unix 毫秒，没有写 0。
func optionalMillis(value *time.Time) int64 {
	if value == nil {
		return 0
	}
	return millis(*value)
}

func targetProtos(raw json.RawMessage) []*strategypb.InstrumentWeight {
	targets, err := trigger.DecodeTargets(raw)
	if err != nil {
		return []*strategypb.InstrumentWeight{}
	}
	out := make([]*strategypb.InstrumentWeight, 0, len(targets))
	for _, target := range targets {
		out = append(out, &strategypb.InstrumentWeight{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight, Market: target.Market})
	}
	return out
}

func itemProtos(items []store.ResultItem) []*strategypb.StrategyResultItem {
	out := make([]*strategypb.StrategyResultItem, 0, len(items))
	for _, item := range items {
		row := &strategypb.StrategyResultItem{RuleId: item.RuleID, InstrumentId: item.InstrumentID, Market: item.Market, Stage: item.Stage, Reason: item.Reason, Weight: dereference(item.Weight)}
		if item.Score != nil {
			row.Score = strconv.FormatFloat(*item.Score, 'g', -1, 64)
		}
		if item.Rank != nil {
			row.Rank = int32(*item.Rank)
		}
		if item.Scale != nil {
			row.Scale = strconv.FormatFloat(*item.Scale, 'g', -1, 64)
		}
		out = append(out, row)
	}
	return out
}

func replayProto(value store.Replay) *strategypb.Replay {
	replay := &strategypb.Replay{
		ReplayId: value.ReplayID, StrategyId: dereference(value.StrategyID), DslYaml: value.DSLYaml, SpaceId: value.SpaceID,
		StartTime: millis(value.StartTime), EndTime: millis(value.EndTime), SpotFeeBps: value.SpotFeeBps, SwapFeeBps: value.SwapFeeBps, SlippageBps: value.SlippageBps, FundingColumn: value.FundingColumn, Status: value.Status,
		MetricsJson: string(value.MetricsJSON), Error: value.Error, CreatedAt: millis(value.CreatedAt), UpdatedAt: millis(value.UpdatedAt),
		DslHash: value.DSLHash, InstanceId: dereference(value.InstanceID), SessionId: dereference(value.SessionID), Calendar: value.Calendar, Input: value.Input, ProgressTime: optionalMillis(value.ProgressTime),
	}
	if len(value.Tasks) > 0 {
		replay.Tasks = make(map[string]*strategypb.ReplayTask, len(value.Tasks))
		for market, task := range value.Tasks {
			replay.Tasks[market] = &strategypb.ReplayTask{FactorTaskId: task.FactorTaskID, DatasetId: task.DatasetID}
		}
	}
	return replay
}

func replayBarProto(value store.ReplayBar) *strategypb.ReplayBar {
	return &strategypb.ReplayBar{
		BarEndTime: millis(value.BarEndTime), Status: value.Status, Targets: targetProtos(value.TargetsJSON), PositionsJson: string(value.PositionsJSON), SummaryJson: string(value.SummaryJSON), ParamsJson: string(value.ParamsJSON),
		BarReturn: value.Return, Equity: value.Equity, Turnover: value.Turnover, Fee: value.Fee, SpotValue: value.SpotValue, SwapValue: value.SwapValue,
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

// millis 把时刻写成 RPC 里的 Unix 毫秒，零时间写 0。
func millis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}
