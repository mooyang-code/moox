package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

var _ archivepb.ArchiveMgrService = (*MgrService)(nil)

// MgrService 实现 trpc.moox.archive.ArchiveMgr。
type MgrService struct{ svc *Service }

const (
	defaultPageSize = 50
	maxPageSize     = 200
	previewRuns     = 5
	timeSeriesKind  = "time_series"
	datasetActive   = "active"
)

func pageOf(p *commonpb.Page) (page, size, offset int) {
	page, size = int(p.GetPage()), int(p.GetSize())
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size, (page - 1) * size
}

func pageResult(page, size, total int) *commonpb.PageResult {
	return &commonpb.PageResult{Page: uint32(page), Size: uint32(size), Total: uint32(total), HasMore: page*size < total, TotalState: commonpb.TotalState_EXACT}
}

// scheduleInput 是创建与修改任务时用户填写的执行计划部分。
type scheduleInput struct {
	Cron, TZ, StartMode, StartAt, BackfillFrom string
}

// builtSchedule 是校验后的执行计划。
type builtSchedule struct {
	Plan         *schedule.Plan
	Interval     time.Duration
	StartAt      time.Time
	BackfillFrom time.Time
	NextRunAt    time.Time
	Immediate    bool
}

// buildSchedule 校验执行计划与开始时间，计算下一个计划时刻。
func (s *Service) buildSchedule(in scheduleInput, retention string, now time.Time) (builtSchedule, error) {
	plan, err := s.plan(in.Cron, in.TZ)
	if err != nil {
		return builtSchedule{}, err
	}
	interval, err := plan.CheckInterval(now, s.cfg.Schedule.MinInterval)
	if err != nil {
		return builtSchedule{}, err
	}
	out := builtSchedule{Plan: plan, Interval: interval}
	switch strings.TrimSpace(in.StartMode) {
	case "", "immediate":
		out.StartAt, out.Immediate = now, true
		out.NextRunAt = plan.Next(now)
	case "at":
		start, err := parseRFC("start_at", in.StartAt)
		if err != nil {
			return builtSchedule{}, err
		}
		if start.IsZero() {
			return builtSchedule{}, errors.New("start_mode=at 时必须填写 start_at")
		}
		out.StartAt = start
		base := start
		if base.Before(now) {
			base = now
		}
		out.NextRunAt = plan.Next(base.Add(-time.Second))
	default:
		return builtSchedule{}, fmt.Errorf("start_mode %q 无效，只能是 immediate 或 at", in.StartMode)
	}
	if out.NextRunAt.IsZero() {
		return builtSchedule{}, errors.New("执行计划没有下一个计划时刻")
	}
	backfill, err := parseRFC("backfill_from", in.BackfillFrom)
	if err != nil {
		return builtSchedule{}, err
	}
	if !backfill.IsZero() {
		if !backfill.Before(now) {
			return builtSchedule{}, errors.New("回补起点必须早于当前时间")
		}
		if earliest, ok := earliestBackfill(retention, now); ok && backfill.Before(earliest) {
			return builtSchedule{}, fmt.Errorf("回补起点不能早于 %s（今天 − 线上保留期 + 1 天）", earliest.Format(time.RFC3339))
		}
		out.BackfillFrom = backfill
	}
	return out, nil
}

// earliestBackfill 返回回补起点的下限；保留期不限时返回 false。
func earliestBackfill(retention string, now time.Time) (time.Time, bool) {
	limit, forever, err := parseRetention(retention)
	if err != nil || forever {
		return time.Time{}, false
	}
	return now.Add(-limit).Add(24 * time.Hour), true
}

// loadArchivableDataset 读取数据集并确认它是已激活的时序数据集。
func (s *Service) loadArchivableDataset(ctx context.Context, spaceID, datasetID string) (Dataset, error) {
	if strings.TrimSpace(datasetID) == "" {
		return Dataset{}, errors.New("dataset_id 不能为空")
	}
	ds, err := s.datasets.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return Dataset{}, err
	}
	if strings.ToLower(ds.DataKind) != timeSeriesKind {
		return Dataset{}, fmt.Errorf("数据集 %s 不是时序数据集，不能归档", datasetID)
	}
	if strings.ToLower(ds.Status) != datasetActive {
		return Dataset{}, fmt.Errorf("数据集 %s 尚未激活（状态 %q），不能归档", datasetID, ds.Status)
	}
	return ds, nil
}

func (m *MgrService) CreateArchiveTask(ctx context.Context, req *archivepb.CreateArchiveTaskReq) (*archivepb.CreateArchiveTaskRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	ds, err := s.loadArchivableDataset(ctx, space, req.GetDatasetId())
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: datasetError(err)}, nil
	}
	unit, err := schedule.PartitionUnit(ds.Freq)
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	now := s.clock()
	built, err := s.buildSchedule(scheduleInput{req.GetScheduleCron(), req.GetScheduleTz(), req.GetStartMode(), req.GetStartAt(), req.GetBackfillFrom()}, ds.Retention, now)
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	prefix, err := s.chooseFilePrefix(ctx, space, ds.DatasetID, ds.Name)
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	task := store.Task{
		FilePrefix: prefix, MetaJSON: metaJSON(ds),
		SpaceID: space, DatasetID: ds.DatasetID, Freq: ds.Freq, PartitionUnit: unit, DataNodeID: ds.DataNodeID, Retention: ds.Retention,
		ScheduleCron: built.Plan.Cron, ScheduleTZ: built.Plan.TZ, IntervalSeconds: int(built.Interval / time.Second), StartAt: built.StartAt,
		BackfillFrom: built.BackfillFrom, Status: store.StatusEnabled, NextRunAt: built.NextRunAt, Creator: "console",
	}
	immediate := built.Immediate
	if req.GetPaused() {
		task.Status, task.NextRunAt, immediate = store.StatusPaused, time.Time{}, false
	}
	created, _, err := s.store.CreateTask(ctx, task, immediate, now)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			if _, gerr := s.store.GetTask(ctx, space, ds.DatasetID); gerr != nil {
				return &archivepb.CreateArchiveTaskRsp{RetInfo: conflict(fmt.Errorf("文件名前缀 %q 与同空间其他任务冲突，请重试", prefix))}, nil
			}
			return &archivepb.CreateArchiveTaskRsp{RetInfo: conflict(fmt.Errorf("数据集 %s 已经有归档任务", ds.DatasetID))}, nil
		}
		return &archivepb.CreateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	s.clearDeleted(space, ds.DatasetID)
	return &archivepb.CreateArchiveTaskRsp{RetInfo: success(), Task: taskToPB(created)}, nil
}

func datasetError(err error) *commonpb.RetInfo {
	if errors.Is(err, ErrDatasetNotFound) {
		return &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND, Msg: err.Error()}
	}
	if strings.Contains(err.Error(), "不是时序数据集") || strings.Contains(err.Error(), "尚未激活") || strings.Contains(err.Error(), "不能为空") {
		return invalid(err)
	}
	return inner(err)
}

func (m *MgrService) UpdateArchiveTask(ctx context.Context, req *archivepb.UpdateArchiveTaskReq) (*archivepb.UpdateArchiveTaskRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	task, err := s.store.GetTask(ctx, space, req.GetDatasetId())
	if err != nil {
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	status := task.Status
	switch req.GetStatus() {
	case "":
	case store.StatusEnabled, store.StatusPaused:
		status = req.GetStatus()
	default:
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: invalid(fmt.Errorf("status %q 无效，只能是 enabled 或 paused", req.GetStatus()))}, nil
	}
	in := scheduleInput{Cron: task.ScheduleCron, TZ: task.ScheduleTZ, StartMode: "at", StartAt: fmtRFC(task.StartAt), BackfillFrom: fmtRFC(task.BackfillFrom)}
	if req.GetScheduleCron() != "" {
		in.Cron = req.GetScheduleCron()
	}
	if req.GetScheduleTz() != "" {
		in.TZ = req.GetScheduleTz()
	}
	if req.GetStartMode() != "" {
		in.StartMode, in.StartAt = req.GetStartMode(), req.GetStartAt()
	}
	if req.GetBackfillFrom() != "" {
		in.BackfillFrom = req.GetBackfillFrom()
	}
	now := s.clock()
	built, err := s.buildSchedule(in, task.Retention, now)
	if err != nil {
		// 已有回补起点超出保留期时不应阻止修改其他设置：只有用户改了回补起点才校验它。
		if req.GetBackfillFrom() == "" && strings.Contains(err.Error(), "回补起点") {
			in.BackfillFrom = ""
			built, err = s.buildSchedule(in, task.Retention, now)
		}
		if err != nil {
			return &archivepb.UpdateArchiveTaskRsp{RetInfo: invalid(err)}, nil
		}
	}
	if req.GetBackfillFrom() == "" {
		// 用户没改回补起点：保留原值（即使它已超出保留期，校验时被忽略），首次回补尚未完成时不能被改丢。
		built.BackfillFrom = task.BackfillFrom
	}
	update := store.TaskUpdate{
		ScheduleCron: built.Plan.Cron, ScheduleTZ: built.Plan.TZ, IntervalSeconds: int(built.Interval / time.Second), StartAt: built.StartAt,
		BackfillFrom: built.BackfillFrom, Status: status, NextRunAt: built.NextRunAt, RunNow: req.GetRunNow(),
	}
	if status == store.StatusPaused {
		update.NextRunAt = time.Time{}
	}
	updated, _, err := s.store.UpdateTask(ctx, space, req.GetDatasetId(), update, now)
	if err != nil {
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.UpdateArchiveTaskRsp{RetInfo: success(), Task: taskToPB(updated)}, nil
}

func (m *MgrService) DeleteArchiveTask(ctx context.Context, req *archivepb.DeleteArchiveTaskReq) (*archivepb.DeleteArchiveTaskRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.DeleteArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	deletedID, err := s.store.DeleteTask(ctx, space, req.GetDatasetId())
	if err != nil {
		if errors.Is(err, store.ErrHasPartitions) {
			return &archivepb.DeleteArchiveTaskRsp{RetInfo: conflict(errors.New("任务已有归档分区，不能删除，只能停用"))}, nil
		}
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.DeleteArchiveTaskRsp{RetInfo: conflict(errors.New("任务有运行中的运行，请先取消"))}, nil
		}
		return &archivepb.DeleteArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	s.markDeleted(space, req.GetDatasetId(), strconv.FormatInt(deletedID, 10))
	return &archivepb.DeleteArchiveTaskRsp{RetInfo: success()}, nil
}

func (m *MgrService) RunArchiveTaskNow(ctx context.Context, req *archivepb.RunArchiveTaskNowReq) (*archivepb.RunArchiveTaskNowRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.RunArchiveTaskNowRsp{RetInfo: invalid(err)}, nil
	}
	run, err := s.store.CreateRun(ctx, space, req.GetDatasetId(), store.KindManual, time.Time{}, time.Time{}, s.clock())
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.RunArchiveTaskNowRsp{RetInfo: conflict(errors.New("任务已有排队中或运行中的运行"))}, nil
		}
		return &archivepb.RunArchiveTaskNowRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.RunArchiveTaskNowRsp{RetInfo: success(), Run: runToPB(run)}, nil
}

func (m *MgrService) BackfillArchiveRange(ctx context.Context, req *archivepb.BackfillArchiveRangeReq) (*archivepb.BackfillArchiveRangeRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: invalid(err)}, nil
	}
	start, err := parseRFC("range_start", req.GetRangeStart())
	if err == nil && start.IsZero() {
		err = errors.New("range_start 不能为空")
	}
	var end time.Time
	if err == nil {
		end, err = parseRFC("range_end", req.GetRangeEnd())
		if err == nil && end.IsZero() {
			err = errors.New("range_end 不能为空")
		}
	}
	if err == nil && !start.Before(end) {
		err = errors.New("range_start 必须早于 range_end")
	}
	now := s.clock()
	if err == nil && end.After(now) {
		err = errors.New("range_end 不能晚于当前时间")
	}
	if err != nil {
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: invalid(err)}, nil
	}
	task, err := s.store.GetTask(ctx, space, req.GetDatasetId())
	if err != nil {
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: fail(err)}, nil
	}
	if limit, forever, perr := parseRetention(task.Retention); perr == nil && !forever && start.Before(now.Add(-limit)) {
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: invalid(fmt.Errorf("补拉区间不能早于线上保留期截止时间 %s", now.Add(-limit).Format(time.RFC3339)))}, nil
	}
	run, err := s.store.CreateRun(ctx, space, req.GetDatasetId(), store.KindRange, start, end, now)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.BackfillArchiveRangeRsp{RetInfo: conflict(errors.New("任务已有排队中或运行中的运行"))}, nil
		}
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.BackfillArchiveRangeRsp{RetInfo: success(), Run: runToPB(run)}, nil
}

func (m *MgrService) CancelArchiveRun(ctx context.Context, req *archivepb.CancelArchiveRunReq) (*archivepb.CancelArchiveRunRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, "", false)
	if err != nil {
		return &archivepb.CancelArchiveRunRsp{RetInfo: invalid(err)}, nil
	}
	run, err := s.store.CancelRun(ctx, req.GetRunId(), space, s.clock())
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.CancelArchiveRunRsp{RetInfo: conflict(errors.New("运行已经结束，不能取消"))}, nil
		}
		return &archivepb.CancelArchiveRunRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.CancelArchiveRunRsp{RetInfo: success(), Run: runToPB(run)}, nil
}

func (m *MgrService) ListArchiveRuns(ctx context.Context, req *archivepb.ListArchiveRunsReq) (*archivepb.ListArchiveRunsRsp, error) {
	space, err := resolveSpace(ctx, req.GetSpaceId(), false)
	if err != nil {
		return &archivepb.ListArchiveRunsRsp{RetInfo: invalid(err)}, nil
	}
	page, size, offset := pageOf(req.GetPage())
	runs, total, err := m.svc.store.ListRuns(ctx, space, req.GetDatasetId(), req.GetStatus(), size, offset)
	if err != nil {
		return &archivepb.ListArchiveRunsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &archivepb.ListArchiveRunsRsp{RetInfo: success(), PageResult: pageResult(page, size, total)}
	for _, run := range runs {
		rsp.Runs = append(rsp.Runs, runToPB(run))
	}
	return rsp, nil
}

func (m *MgrService) GetArchiveRun(ctx context.Context, req *archivepb.GetArchiveRunReq) (*archivepb.GetArchiveRunRsp, error) {
	space, err := resolveSpace(ctx, "", false)
	if err != nil {
		return &archivepb.GetArchiveRunRsp{RetInfo: invalid(err)}, nil
	}
	run, err := m.svc.store.GetRun(ctx, req.GetRunId())
	if err == nil && space != "" && run.SpaceID != space {
		err = store.ErrNotFound
	}
	if err != nil {
		return &archivepb.GetArchiveRunRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.GetArchiveRunRsp{RetInfo: success(), Run: runToPB(run)}, nil
}

func (m *MgrService) ListArchivePartitions(ctx context.Context, req *archivepb.ListArchivePartitionsReq) (*archivepb.ListArchivePartitionsRsp, error) {
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.ListArchivePartitionsRsp{RetInfo: invalid(err)}, nil
	}
	fromKey, toKey := req.GetFromMonth(), req.GetToMonth()
	for _, month := range []string{fromKey, toKey} {
		if month != "" {
			if _, perr := time.Parse("2006-01", month); perr != nil {
				return &archivepb.ListArchivePartitionsRsp{RetInfo: invalid(fmt.Errorf("月份 %q 必须是 YYYY-MM", month))}, nil
			}
		}
	}
	if toKey != "" {
		toKey += "-99" // 日分区键为 YYYY-MM-DD，月分区键为 YYYY-MM，都不大于 YYYY-MM-99
	}
	page, size, offset := pageOf(req.GetPage())
	parts, total, err := m.svc.store.ListPartitions(ctx, space, req.GetDatasetId(), fromKey, toKey, size, offset)
	if err != nil {
		return &archivepb.ListArchivePartitionsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &archivepb.ListArchivePartitionsRsp{RetInfo: success(), PageResult: pageResult(page, size, total)}
	for _, p := range parts {
		rsp.Partitions = append(rsp.Partitions, partitionToPB(p))
	}
	return rsp, nil
}

func (m *MgrService) GetArchivePartition(ctx context.Context, req *archivepb.GetArchivePartitionReq) (*archivepb.GetArchivePartitionRsp, error) {
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.GetArchivePartitionRsp{RetInfo: invalid(err)}, nil
	}
	p, err := m.svc.store.GetPartition(ctx, space, req.GetDatasetId(), req.GetPartitionKey())
	if err != nil {
		return &archivepb.GetArchivePartitionRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.GetArchivePartitionRsp{RetInfo: success(), Partition: partitionToPB(p)}, nil
}

func (m *MgrService) PrepareArchiveTask(ctx context.Context, req *archivepb.PrepareArchiveTaskReq) (*archivepb.PrepareArchiveTaskRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.PrepareArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	ds, err := s.loadArchivableDataset(ctx, space, req.GetDatasetId())
	if err != nil {
		return &archivepb.PrepareArchiveTaskRsp{RetInfo: datasetError(err)}, nil
	}
	unit, err := schedule.PartitionUnit(ds.Freq)
	if err != nil {
		return &archivepb.PrepareArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	spec, tz := schedule.DefaultPlan(space, ds.Freq)
	rsp := &archivepb.PrepareArchiveTaskRsp{
		RetInfo: success(), Freq: ds.Freq, PartitionUnit: unit, Retention: ds.Retention, DataNodeId: ds.DataNodeID,
		RecommendedCron: spec, RecommendedTz: tz,
	}
	if earliest, ok := earliestBackfill(ds.Retention, s.clock()); ok {
		rsp.EarliestBackfillFrom = fmtRFC(earliest)
	}
	if _, err := s.store.GetTask(ctx, space, ds.DatasetID); err == nil {
		rsp.Exists = true
	}
	return rsp, nil
}

func (m *MgrService) PreviewArchiveSchedule(ctx context.Context, req *archivepb.PreviewArchiveScheduleReq) (*archivepb.PreviewArchiveScheduleRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	ds, err := s.loadArchivableDataset(ctx, space, req.GetDatasetId())
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: datasetError(err)}, nil
	}
	now := s.clock()
	built, err := s.buildSchedule(scheduleInput{req.GetScheduleCron(), req.GetScheduleTz(), req.GetStartMode(), req.GetStartAt(), ""}, ds.Retention, now)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	span, err := schedule.ReadSpan(built.Interval, s.cfg.Schedule.MinLookback, ds.Freq)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	rsp := &archivepb.PreviewArchiveScheduleRsp{RetInfo: success(), IntervalSeconds: uint32(built.Interval / time.Second), ReadSpanSeconds: uint32(span / time.Second)}
	runs := built.Plan.NextN(built.NextRunAt.Add(-time.Second), previewRuns)
	for _, t := range runs {
		rsp.NextRuns = append(rsp.NextRuns, fmtRFC(t))
	}
	rsp.Warnings = schedule.AvoidanceWarnings(runs)
	rsp.PartitionExportEstimates = s.exportEstimates(space, ds, built.Plan, now)
	return rsp, nil
}

// exportEstimates 给出上一个分区与当前分区预计的导出时间。
func (s *Service) exportEstimates(space string, ds Dataset, plan *schedule.Plan, now time.Time) []string {
	unit, err := schedule.PartitionUnit(ds.Freq)
	if err != nil {
		return nil
	}
	spaceCfg := s.cfg.Space(space)
	loc, err := time.LoadLocation(spaceCfg.TZ)
	if err != nil {
		loc = time.UTC
	}
	currentKey, currentStart, currentEnd := schedule.PartitionOf(unit, loc, now)
	previousKey, previousStart, previousEnd := schedule.PartitionOf(unit, loc, currentStart.Add(-time.Second))
	var out []string
	for _, c := range []struct {
		key        string
		start, end time.Time
	}{{previousKey, previousStart, previousEnd}, {currentKey, currentStart, currentEnd}} {
		closeAt, err := schedule.PartitionClose(unit, loc, spaceCfg.LocalClose, ds.Freq, c.start, c.end)
		if err != nil {
			continue
		}
		if at := plan.ExportEstimate(closeAt, s.cfg.Schedule.MinLookback); !at.IsZero() {
			out = append(out, fmt.Sprintf("分区 %s：预计 %s 导出", c.key, fmtRFC(at)))
		}
	}
	return out
}

func (m *MgrService) ListArchiveTasks(ctx context.Context, req *archivepb.ListArchiveTasksReq) (*archivepb.ListArchiveTasksRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), false)
	if err != nil {
		return &archivepb.ListArchiveTasksRsp{RetInfo: invalid(err)}, nil
	}
	page, size, offset := pageOf(req.GetPage())
	tasks, total, err := s.store.ListTasks(ctx, space, size, offset)
	if err != nil {
		return &archivepb.ListArchiveTasksRsp{RetInfo: inner(err)}, nil
	}
	views, err := s.taskViews(ctx, space, tasks)
	if err != nil {
		return &archivepb.ListArchiveTasksRsp{RetInfo: inner(err)}, nil
	}
	return &archivepb.ListArchiveTasksRsp{RetInfo: success(), Tasks: views, PageResult: pageResult(page, size, total)}, nil
}

// taskViews 补齐任务的最近运行、分区汇总与健康等级。
func (s *Service) taskViews(ctx context.Context, space string, tasks []store.Task) ([]*archivepb.ArchiveTask, error) {
	now := s.clock()
	lastRuns, err := s.store.LastRuns(ctx, space)
	if err != nil {
		return nil, err
	}
	sums, err := s.store.PartitionSummaries(ctx, space)
	if err != nil {
		return nil, err
	}
	open, err := s.store.ListOpenPartitions(ctx, space)
	if err != nil {
		return nil, err
	}
	openByTask := map[string][]store.Partition{}
	for _, p := range open {
		key := p.SpaceID + "/" + p.DatasetID
		openByTask[key] = append(openByTask[key], p)
	}
	queued, err := s.oldestQueued(ctx, space)
	if err != nil {
		return nil, err
	}
	active, lastSeen, err := s.workerStatus(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]*archivepb.ArchiveTask, 0, len(tasks))
	for _, task := range tasks {
		key := task.SpaceID + "/" + task.DatasetID
		view := taskToPB(task)
		in := healthInput{Now: now, Task: task, OpenParts: openByTask[key], OldestQueued: queued[key], WorkerActive: active, WorkerLastSeen: lastSeen,
			Lookback: s.cfg.Schedule.MinLookback, SpaceTZ: s.cfg.Space(task.SpaceID).TZ, SpaceLocalClose: s.cfg.Space(task.SpaceID).LocalClose}
		if run, ok := lastRuns[key]; ok {
			copied := run
			in.LastRun = &copied
			view.LastRun = runToPB(run)
		}
		if sum, ok := sums[key]; ok {
			view.PartitionCount, view.FileCount, view.TotalRows, view.TotalFileBytes = uint32(sum.Partitions), uint32(sum.Files), sum.Rows, sum.FileBytes
			view.MinDataTime, view.MaxDataTime = fmtRFC(sum.MinDataTime), fmtRFC(sum.MaxDataTime)
		}
		view.Health, view.HealthReason = evaluateHealth(in)
		out = append(out, view)
	}
	return out, nil
}

// oldestQueued 返回每个任务最早一条排队中运行的计划时刻。
func (s *Service) oldestQueued(ctx context.Context, space string) (map[string]time.Time, error) {
	runs, err := s.store.ListActiveRuns(ctx, space)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, run := range runs {
		if run.Status != store.RunQueued {
			continue
		}
		key := run.SpaceID + "/" + run.DatasetID
		if prev, ok := out[key]; !ok || run.ScheduledAt.Before(prev) {
			out[key] = run.ScheduledAt
		}
	}
	return out, nil
}

// workerStatus 返回是否有活动 worker，以及最近一次任一 worker 的心跳时间。
func (s *Service) workerStatus(ctx context.Context, now time.Time) (active bool, lastSeen time.Time, err error) {
	workers, err := s.store.ListWorkers(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	for _, w := range workers {
		if w.Active(now) {
			active = true
		}
		if w.LastHeartbeat.After(lastSeen) {
			lastSeen = w.LastHeartbeat
		}
	}
	return active, lastSeen, nil
}

func (m *MgrService) GetArchiveOverview(ctx context.Context, _ *archivepb.GetArchiveOverviewReq) (*archivepb.GetArchiveOverviewRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, "", false)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: invalid(err)}, nil
	}
	now := s.clock()
	rsp := &archivepb.GetArchiveOverviewRsp{RetInfo: success()}
	workers, err := s.store.ListWorkers(ctx)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: inner(err)}, nil
	}
	var activeWorker string
	for _, w := range workers {
		info := workerToPB(w, now)
		if w.Active(now) && rsp.Worker == nil {
			rsp.Worker = info
			activeWorker = w.WorkerID
		} else {
			rsp.StaleWorkers = append(rsp.StaleWorkers, info)
		}
	}
	if rsp.Worker == nil && len(rsp.StaleWorkers) > 0 {
		rsp.Worker = rsp.StaleWorkers[0]
		rsp.StaleWorkers = rsp.StaleWorkers[1:]
	}
	active, err := s.store.ListActiveRuns(ctx, space)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: inner(err)}, nil
	}
	for _, run := range active {
		switch run.Status {
		case store.RunRunning:
			rsp.RunningRuns = append(rsp.RunningRuns, runToPB(run))
		case store.RunQueued:
			rsp.QueuedRunCount++
		}
	}
	tasks, total, err := s.store.ListTasks(ctx, space, 10000, 0)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: inner(err)}, nil
	}
	rsp.TaskCount = uint32(total)
	for _, t := range tasks {
		if t.Status == store.StatusEnabled {
			rsp.EnabledTaskCount++
		}
	}
	views, err := s.taskViews(ctx, space, tasks)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: inner(err)}, nil
	}
	for _, v := range views {
		if v.GetHealth() == HealthError || v.GetHealth() == HealthWarning {
			rsp.Alerts = append(rsp.Alerts, fmt.Sprintf("%s：%s", v.GetDatasetId(), v.GetHealthReason()))
		}
	}
	if rsp.EnabledTaskCount > 0 && activeWorker == "" {
		rsp.Alerts = append(rsp.Alerts, "没有活动的归档 worker")
	}
	owners, err := s.store.PartitionWorkers(ctx)
	if err == nil && activeWorker != "" {
		for _, t := range tasks {
			if owner, ok := owners[t.SpaceID+"/"+t.DatasetID]; ok && owner != activeWorker {
				rsp.Alerts = append(rsp.Alerts, fmt.Sprintf("%s：分区登记在 worker %s，当前活动 worker 是 %s，运行不会派给它", t.DatasetID, owner, activeWorker))
			}
		}
	}
	sort.Strings(rsp.Alerts)
	return rsp, nil
}
