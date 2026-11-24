package fetchplan

import (
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// StreamPlanItem 是分给推送或本地执行端的一项计划。
type StreamPlanItem struct {
	Name     string
	Executor string
	Market   string
	Plan     *marketfetch.Plan
}

// StreamPlanState 返回 collector-stream 此刻应应用的计划号与各项：未生效的计划已开始下发（StreamPushed）时给它的 Target，
// 否则给最近一份已生效计划的 Target；空间暂停时下发空计划（停止推送接收与本地拉取）。
func StreamPlanState(open, applied *store.FetchPlanRecord, paused bool) (int64, []StreamPlanItem, error) {
	if paused {
		return 0, nil, nil
	}
	record := applied
	if open != nil {
		progress, err := decodeFetchPlanProgress(open.Progress)
		if err != nil {
			return 0, nil, err
		}
		if progress.StreamPushed {
			record = open
		}
	}
	if record == nil {
		return 0, nil, nil
	}
	content, err := decodeFetchPlanContent(record.Plan)
	if err != nil {
		return 0, nil, err
	}
	var items []StreamPlanItem
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if function.IsSCF() || function.Target == nil || len(function.Target.Datasets) == 0 {
			continue
		}
		items = append(items, StreamPlanItem{Name: name, Executor: function.Executor, Market: function.Target.Market.MarketType, Plan: function.Target})
	}
	return record.ID, items, nil
}
