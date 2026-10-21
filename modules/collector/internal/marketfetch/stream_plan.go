package marketfetch

import (
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// StreamPlanItem 是分给推送或本地执行端的一项计划。
type StreamPlanItem struct {
	Name     string
	Executor string
	Market   string
	Plan     *Plan
}

// StreamPlanState 返回 collector-stream 此刻应应用的计划版本与各项：未完成的计划已进入下发阶段时用它的过渡或最终计划，
// 否则用最近一份已生效计划的最终计划；空间暂停时下发空计划（停止推送接收与本地拉取）。
func StreamPlanState(open, applied *store.FetchPlanRecord, paused bool) (int64, string, []StreamPlanItem, error) {
	if paused {
		return 0, StreamStagePaused, nil, nil
	}
	record, stage := applied, StreamStageFinal
	if open != nil {
		progress, err := decodeFetchPlanProgress(open.Progress)
		if err != nil {
			return 0, "", nil, err
		}
		if progress.StreamStage != "" {
			record, stage = open, progress.StreamStage
		}
	}
	if record == nil {
		return 0, "", nil, nil
	}
	content, err := decodeFetchPlanContent(record.Plan)
	if err != nil {
		return 0, "", nil, err
	}
	var items []StreamPlanItem
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if function.IsSCF() {
			continue
		}
		plan := content.FinalPlan(function)
		if stage == StreamStageTransition {
			plan = content.TransitionalPlan(function)
		}
		if plan == nil || len(plan.Datasets) == 0 {
			continue
		}
		items = append(items, StreamPlanItem{Name: name, Executor: function.Executor, Market: plan.Market.MarketType, Plan: plan})
	}
	return record.ID, stage, items, nil
}
