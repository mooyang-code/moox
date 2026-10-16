package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

// pendingPlanWarn 是未生效计划的告警时长：正常的计划变更在几个对账周期内完成。
const pendingPlanWarn = 30 * time.Minute

// GetCollectorStatus 返回空间的运行状态：计划概要、总体健康、数据集健康、函数分配与补采汇总。
func (s *Service) GetCollectorStatus(ctx context.Context, req *pb.GetCollectorStatusReq) (*pb.GetCollectorStatusRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	now := time.Now().UTC()
	plans := s.persistence.FetchPlans()
	backfill := s.persistence.Backfill()
	applied, err := plans.LatestApplied(ctx, spaceID)
	if err != nil {
		return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	open, err := plans.Open(ctx, spaceID)
	if err != nil {
		return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.GetCollectorStatusRsp{RetInfo: retOK(), Level: klinecheck.HealthOK, Open: planSummary(open), Applied: planSummary(applied), GeneratedAt: now.Format(time.RFC3339)}
	raise := func(level, reason string) {
		rsp.Level = klinecheck.WorseLevel(rsp.Level, level)
		rsp.Reasons = append(rsp.Reasons, reason)
	}

	// 以未生效计划为准展示正在变化的状态，没有则展示已生效的计划。
	source := applied
	if open != nil {
		source = open
	}
	if source != nil {
		if rsp.Functions, err = planFunctions(source); err != nil {
			return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		if lastError := strings.TrimSpace(source.LastError); lastError != "" {
			raise(klinecheck.HealthError, fmt.Sprintf("采集计划 #%d 下发失败：%s", source.ID, lastError))
		}
	}
	if open != nil && now.Sub(open.CreatedAt) > pendingPlanWarn {
		raise(klinecheck.HealthWarn, fmt.Sprintf("采集计划 #%d 已 %s 未生效", open.ID, klinecheck.FormatDuration(now.Sub(open.CreatedAt))))
	}

	health, err := klinecheck.Health(ctx, plans, backfill, spaceID, now)
	if err != nil {
		return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	names, err := s.taskRepo.ResultDatasetNames(ctx, spaceID)
	if err != nil {
		return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	functionCounts := map[string]int{}
	for _, function := range rsp.Functions {
		for _, key := range function.GetDatasets() {
			functionCounts[key]++
		}
	}
	for _, item := range health {
		key := klinecheck.DatasetKey(item.DatasetID, item.Frequency)
		status := datasetStatusToPB(item, functionCounts[key])
		status.DatasetName = names[item.DatasetID]
		rsp.Datasets = append(rsp.Datasets, status)
		label := status.DatasetName
		if label == "" {
			label = key
		}
		for _, reason := range item.Reasons {
			raise(item.Level, label+"："+reason)
		}
	}

	counts, err := backfill.JobCounts(ctx, spaceID, now.Add(-24*time.Hour))
	if err != nil {
		return &pb.GetCollectorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp.ActiveBackfills, rsp.FailedBackfills_24H = int32(counts.Active), int32(counts.Failed)
	return rsp, nil
}

func planSummary(record *store.FetchPlanRecord) *pb.FetchPlanSummary {
	if record == nil {
		return nil
	}
	return &pb.FetchPlanSummary{Id: record.ID, Status: record.Status, LastError: record.LastError, CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339)}
}

func datasetStatusToPB(item klinecheck.DatasetHealth, functions int) *pb.CollectorDatasetStatus {
	out := &pb.CollectorDatasetStatus{
		DatasetId: item.DatasetID, Frequency: item.Frequency, SubjectCount: int32(item.Subjects), FunctionCount: int32(functions),
		EffectiveFrom: item.EffectiveFrom.UTC().Format(time.RFC3339), InspectLagSeconds: int64(item.InspectLag / time.Second),
		GapBars_24H: int32(item.GapBars), GapSubjects_24H: int32(item.GapSubjects), FailedChunks_24H: int32(item.FailedChunks),
		AbsentBars: int32(item.AbsentBars), Level: item.Level, Reasons: item.Reasons,
	}
	if !item.InspectedUntil.IsZero() {
		out.InspectedUntil = item.InspectedUntil.UTC().Format(time.RFC3339)
	}
	if history := item.History; history != nil {
		out.HistoryScannedAt = history.ScannedAt.UTC().Format(time.RFC3339)
		out.HistoryGapBars, out.HistoryGapSubjects, out.HistoryError = int32(history.GapBars), int32(history.GapSubjects), history.LastError
	}
	return out
}

// planFunctions 返回计划里每个函数的分配与状态，下发中的排前。
func planFunctions(source *store.FetchPlanRecord) ([]*pb.CollectorFunctionStatus, error) {
	content, err := marketfetch.DecodeFetchPlanContent(source.Plan)
	if err != nil {
		return nil, err
	}
	progress, err := marketfetch.DecodeFetchPlanProgress(source.Progress)
	if err != nil {
		return nil, err
	}
	appliedPlan := source.Status == store.FetchPlanApplied
	var out []*pb.CollectorFunctionStatus
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		plan := content.TransitionalPlan(function)
		if plan == nil {
			continue
		}
		state := &pb.CollectorFunctionStatus{Name: name, Region: function.Region, Market: function.Market, State: "applied", Cron: content.TransitionalCron(function)}
		switch {
		case function.New == nil:
			if appliedPlan {
				continue
			}
			state.State = "retiring"
		case !appliedPlan && function.Push && !progress.Finalized[name]:
			state.State = "pending"
		}
		if final := content.FinalPlan(function); appliedPlan && final != nil {
			plan = final
			state.Cron = content.FinalCron(function)
		}
		for _, dataset := range plan.Datasets {
			if len(dataset.Copies) == 0 {
				continue
			}
			last := dataset.Copies[len(dataset.Copies)-1]
			key := marketfetch.PlanDatasetKeyOf(dataset)
			state.SubjectCount += int32(len(last.Subjects))
			state.Datasets = append(state.Datasets, key)
			detail := &pb.CollectorFunctionDataset{Dataset: key, Subjects: make([]string, 0, len(last.Subjects))}
			if last.From > 0 {
				detail.EffectiveFrom = time.Unix(last.From, 0).UTC().Format(time.RFC3339)
			}
			for _, subject := range last.Subjects {
				detail.Subjects = append(detail.Subjects, subject.ID)
			}
			state.DatasetSubjects = append(state.DatasetSubjects, detail)
		}
		out = append(out, state)
	}
	sort.SliceStable(out, func(i, j int) bool { return functionRank(out[i].GetState()) > functionRank(out[j].GetState()) })
	return out, nil
}

func functionRank(state string) int {
	switch state {
	case "pending":
		return 2
	case "retiring":
		return 1
	}
	return 0
}
