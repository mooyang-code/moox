package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	previewPulls    = 5
	previewReviews  = 3
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

// backfillClampSlack 是回补起点、补拉起点早于下界多少以内时夹到下界而不报错：Prepare 给出的最早值按当时的 now 计算，
// 用户填写、提交时 now 已经后移，原样校验必被拒。
const backfillClampSlack = time.Hour

// clampToEarliest 把早于下界 earliest 不到 backfillClampSlack 的 start 夹到下界（向上取整到秒）；ok 为假表示差得更多，应拒绝。
func clampToEarliest(start, earliest time.Time) (time.Time, bool) {
	if !start.Before(earliest) {
		return start, true
	}
	if earliest.Sub(start) >= backfillClampSlack {
		return start, false
	}
	clamped := earliest.Truncate(time.Second)
	if clamped.Before(earliest) {
		clamped = clamped.Add(time.Second)
	}
	return clamped, true
}

// validateBackfill 校验回补起点：必须早于当前时间，且不早于「今天 − 线上保留期 + 1 天」（保留期为 forever 时不限）；
// 早于下界不到 1 小时的夹到下界，返回的就是实际保存的值。
func validateBackfill(value, retention string, now time.Time) (time.Time, error) {
	backfill, err := parseRFC("backfill_from", value)
	if err != nil || backfill.IsZero() {
		return backfill, err
	}
	if !backfill.Before(now) {
		return time.Time{}, fmt.Errorf("%w：必须早于当前时间", ErrInvalidBackfill)
	}
	earliest, ok, err := earliestBackfill(retention, now)
	if err != nil {
		return time.Time{}, err
	}
	if ok {
		clamped, within := clampToEarliest(backfill, earliest)
		if !within {
			return time.Time{}, fmt.Errorf("%w：不能早于 %s（今天 − 线上保留期 + 1 天）", ErrInvalidBackfill, earliest.Format(time.RFC3339))
		}
		backfill = clamped
	}
	return backfill, nil
}

// pullEveryBars 校验用户填写的 N：0 取 fallback；1d 及以上每天一次，固定存 1。
func pullEveryBars(freq string, requested uint32, fallback int) (int, error) {
	daily, err := schedule.Daily(freq)
	if err != nil {
		return 0, err
	}
	n := int(requested)
	if n == 0 {
		n = fallback
	}
	if err := schedule.ValidatePullEveryBars(n); err != nil {
		return 0, err
	}
	if daily {
		return 1, nil
	}
	// 步长不超过一天：步长太长时到点拉取要读的根数会超出线上保留期，被截断后误记为无法补齐。
	span, err := schedule.BarSpan(freq)
	if err != nil {
		return 0, err
	}
	if span*time.Duration(n) > 24*time.Hour {
		return 0, fmt.Errorf("pull_every_bars × 频率不能超过 1 天：%s × %d", freq, n)
	}
	return n, nil
}

// earliestBackfill 返回回补起点的下限；保留期为 forever 时返回 false。
func earliestBackfill(retention string, now time.Time) (time.Time, bool, error) {
	limit, forever, err := parseRetention(retention)
	if err != nil || forever {
		return time.Time{}, false, err
	}
	return now.Add(-limit).Add(24 * time.Hour), true, nil
}

// ErrInvalidDataset 表示数据集不能归档（不是时序数据集、未激活或 ID 为空）。
var ErrInvalidDataset = errors.New("数据集不能归档")

// ErrInvalidBackfill 表示回补起点不合法。
var ErrInvalidBackfill = errors.New("回补起点不合法")

// loadArchivableDataset 读取数据集并确认它是已激活的时序数据集。
func (s *Service) loadArchivableDataset(ctx context.Context, spaceID, datasetID string) (Dataset, error) {
	if strings.TrimSpace(datasetID) == "" {
		return Dataset{}, fmt.Errorf("%w：dataset_id 不能为空", ErrInvalidDataset)
	}
	ds, err := s.datasets.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return Dataset{}, err
	}
	if strings.ToLower(ds.DataKind) != timeSeriesKind {
		return Dataset{}, fmt.Errorf("%w：数据集 %s 不是时序数据集", ErrInvalidDataset, datasetID)
	}
	if strings.ToLower(ds.Status) != datasetActive {
		return Dataset{}, fmt.Errorf("%w：数据集 %s 尚未激活（状态 %q）", ErrInvalidDataset, datasetID, ds.Status)
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
	interval, err := schedule.ReviewInterval(ds.Freq)
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	n, err := pullEveryBars(ds.Freq, req.GetPullEveryBars(), schedule.DefaultPullEveryBars)
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	backfill, err := validateBackfill(req.GetBackfillFrom(), ds.Retention, s.clock())
	if err != nil {
		return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	task := store.Task{
		MetaJSON: metaJSON(ds),
		SpaceID:  space, DatasetID: ds.DatasetID, Freq: ds.Freq, PartitionUnit: unit, DataNodeID: ds.DataNodeID, Retention: ds.Retention,
		PullEveryBars: n, IntervalSeconds: int(interval / time.Second), BackfillFrom: backfill, Status: store.StatusEnabled, Creator: "console",
	}
	if req.GetPaused() {
		task.Status = store.StatusPaused
	}
	created, err := s.store.CreateTask(ctx, task)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.CreateArchiveTaskRsp{RetInfo: conflict(fmt.Errorf("数据集 %s 已经有归档任务", ds.DatasetID))}, nil
		}
		return &archivepb.CreateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.CreateArchiveTaskRsp{RetInfo: success(), Task: s.taskToPB(created, s.clock())}, nil
}

func datasetError(err error) *commonpb.RetInfo {
	if errors.Is(err, ErrDatasetNotFound) {
		return &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND, Msg: err.Error()}
	}
	if errors.Is(err, ErrInvalidDataset) {
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
	n, err := pullEveryBars(task.Freq, req.GetPullEveryBars(), task.PullEveryBars)
	if err != nil {
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	now := s.clock()
	// 用户没改回补起点时保留原值（即使它已超出保留期），首次回补尚未完成时不能被改丢；只有改了才校验。
	backfill := task.BackfillFrom
	if req.GetBackfillFrom() != "" {
		if backfill, err = validateBackfill(req.GetBackfillFrom(), task.Retention, now); err != nil {
			return &archivepb.UpdateArchiveTaskRsp{RetInfo: invalid(err)}, nil
		}
	}
	update := store.TaskUpdate{PullEveryBars: n, BackfillFrom: backfill, Status: status, ClearUnrecoverable: req.GetClearUnrecoverable()}
	updated, err := s.store.UpdateTask(ctx, space, req.GetDatasetId(), update, now)
	if err != nil {
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.UpdateArchiveTaskRsp{RetInfo: success(), Task: s.taskToPB(updated, now)}, nil
}

func (m *MgrService) DeleteArchiveTask(ctx context.Context, req *archivepb.DeleteArchiveTaskReq) (*archivepb.DeleteArchiveTaskRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.DeleteArchiveTaskRsp{RetInfo: invalid(err)}, nil
	}
	_, err = s.store.DeleteTask(ctx, space, req.GetDatasetId())
	if err != nil {
		if errors.Is(err, store.ErrHasPartitions) {
			return &archivepb.DeleteArchiveTaskRsp{RetInfo: conflict(errors.New("任务已有归档分区，不能删除，只能停用"))}, nil
		}
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.DeleteArchiveTaskRsp{RetInfo: conflict(errors.New("任务有运行中的运行，请先取消"))}, nil
		}
		return &archivepb.DeleteArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
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
	limit, forever, perr := parseRetention(task.Retention)
	if perr != nil {
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: invalid(perr)}, nil
	}
	// 和回补起点一样留一天余量：运行排队开始时截止点已经后移，贴着截止点的起点会被截掉一截、误记为无法补齐。
	// 早于下界不到 1 小时的夹到下界（下发的就是夹取后的区间）。
	if earliest := now.Add(-limit).Add(24 * time.Hour); !forever {
		clamped, within := clampToEarliest(start, earliest)
		if !within {
			return &archivepb.BackfillArchiveRangeRsp{RetInfo: invalid(fmt.Errorf("补拉区间不能早于 %s（今天 − 线上保留期 + 1 天）", earliest.Format(time.RFC3339)))}, nil
		}
		if !clamped.Before(end) {
			return &archivepb.BackfillArchiveRangeRsp{RetInfo: invalid(fmt.Errorf("补拉区间夹到下界 %s 之后为空", clamped.Format(time.RFC3339)))}, nil
		}
		start = clamped
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
	runs, total, err := m.svc.store.ListRuns(ctx, space, req.GetDatasetId(), req.GetStatus(), req.GetIncludeTicks(), size, offset)
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
	status, err := m.svc.workerStatus(ctx, m.svc.clock())
	if err != nil {
		return &archivepb.ListArchivePartitionsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &archivepb.ListArchivePartitionsRsp{RetInfo: success(), PageResult: pageResult(page, size, total)}
	for _, p := range parts {
		rsp.Partitions = append(rsp.Partitions, partitionToPB(p, status.remoteEnabled))
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
	status, err := m.svc.workerStatus(ctx, m.svc.clock())
	if err != nil {
		return &archivepb.GetArchivePartitionRsp{RetInfo: inner(err)}, nil
	}
	return &archivepb.GetArchivePartitionRsp{RetInfo: success(), Partition: partitionToPB(p, status.remoteEnabled)}, nil
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
	rsp := &archivepb.PrepareArchiveTaskRsp{
		RetInfo: success(), Freq: ds.Freq, PartitionUnit: unit, Retention: ds.Retention, DataNodeId: ds.DataNodeID,
		RecommendedPullEveryBars: schedule.DefaultPullEveryBars,
	}
	if earliest, ok, _ := earliestBackfill(ds.Retention, s.clock()); ok {
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
	n, err := pullEveryBars(ds.Freq, req.GetPullEveryBars(), schedule.DefaultPullEveryBars)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	interval, err := schedule.ReviewInterval(ds.Freq)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	span, err := schedule.ReadSpan(interval, s.cfg.Schedule.MinLookback, ds.Freq)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	now := s.clock()
	timing := s.timing(space, ds.Freq, n)
	pulls, err := schedule.NextPulls(timing, now, previewPulls)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	reviews, err := schedule.NextReviews(timing, now, previewReviews)
	if err != nil {
		return &archivepb.PreviewArchiveScheduleRsp{RetInfo: invalid(err)}, nil
	}
	rsp := &archivepb.PreviewArchiveScheduleRsp{RetInfo: success(), IntervalSeconds: uint32(interval / time.Second), ReadSpanSeconds: uint32(span / time.Second)}
	for _, t := range pulls {
		rsp.NextRuns = append(rsp.NextRuns, fmtRFC(t))
	}
	for _, t := range reviews {
		rsp.NextReviews = append(rsp.NextReviews, fmtRFC(t))
	}
	rsp.PartitionExportEstimates = s.exportEstimates(space, ds, timing, now)
	return rsp, nil
}

// exportEstimates 给出上一个分区与当前分区预计的导出时间：收盘 + 最少回看之后的第一次拉取或复核。
func (s *Service) exportEstimates(space string, ds Dataset, timing schedule.Timing, now time.Time) []string {
	unit, err := schedule.PartitionUnit(ds.Freq)
	if err != nil {
		return nil
	}
	spaceCfg := s.cfg.Space(space)
	currentKey, currentStart, currentEnd := schedule.PartitionOf(unit, timing.Loc, now)
	previousKey, previousStart, previousEnd := schedule.PartitionOf(unit, timing.Loc, currentStart.Add(-time.Second))
	var out []string
	for _, c := range []struct {
		key        string
		start, end time.Time
	}{{previousKey, previousStart, previousEnd}, {currentKey, currentStart, currentEnd}} {
		closeAt, err := schedule.PartitionClose(unit, timing.Loc, spaceCfg.LocalClose, ds.Freq, c.start, c.end)
		if err != nil {
			continue
		}
		if at, err := schedule.ExportEstimate(timing, closeAt, s.cfg.Schedule.MinLookback); err == nil {
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
	lastReviews, err := s.store.LastReviewRuns(ctx, space)
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
	status, err := s.workerStatus(ctx, now)
	if err != nil {
		return nil, err
	}
	var backlogs map[string]store.RemoteBacklog
	if status.remoteEnabled {
		if backlogs, err = s.store.RemoteBacklogs(ctx, space); err != nil {
			return nil, err
		}
	}
	out := make([]*archivepb.ArchiveTask, 0, len(tasks))
	for _, task := range tasks {
		key := task.SpaceID + "/" + task.DatasetID
		view := s.taskToPB(task, now)
		in := healthInput{Now: now, Task: task, OpenParts: openByTask[key], OldestQueued: queued[key], WorkerActive: status.active, WorkerLastSeen: status.lastSeen,
			Lookback: s.cfg.Schedule.MinLookback, SpaceTZ: s.cfg.Space(task.SpaceID).TZ, SpaceLocalClose: s.cfg.Space(task.SpaceID).LocalClose,
			RemoteEnabled: status.remoteEnabled, Remote: backlogs[key]}
		if run, ok := lastRuns[key]; ok {
			copied := run
			in.LastRun = &copied
			view.LastRun = runToPB(run)
		}
		if run, ok := lastReviews[key]; ok {
			copied := run
			in.LastReview = &copied
		}
		if sum, ok := sums[key]; ok {
			view.PartitionCount, view.FileCount, view.TotalRows, view.TotalFileBytes = uint32(sum.Partitions), uint32(sum.Files), sum.Rows, sum.FileBytes
			view.MinDataTime, view.MaxDataTime = fmtRFC(sum.MinDataTime), fmtRFC(sum.MaxDataTime)
			if status.remoteEnabled {
				pending := backlogs[key].Unreplicated
				view.RemotePendingCount, view.RemoteVerifiedCount = uint32(pending), uint32(max(sum.Files-pending, 0))
			}
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

// workerState 是 worker 的汇总状态。
type workerState struct {
	// active：有活动 worker；lastSeen：任一 worker 最近一次心跳的时间。
	active   bool
	lastSeen time.Time
	// remoteEnabled：对象存储已启用。有活动 worker 时看它，没有时看最近一次心跳的那条 worker 记录；没有任何记录时视为未启用。
	remoteEnabled bool
}

// workerStatus 汇总 worker 的状态。
func (s *Service) workerStatus(ctx context.Context, now time.Time) (workerState, error) {
	workers, err := s.store.ListWorkers(ctx)
	if err != nil {
		return workerState{}, err
	}
	var out workerState
	var reference *store.Worker
	for i, w := range workers {
		switch {
		case w.Active(now) && (reference == nil || !reference.Active(now)):
			reference = &workers[i]
		case reference == nil || !reference.Active(now) && w.LastHeartbeat.After(reference.LastHeartbeat):
			reference = &workers[i]
		}
		if w.Active(now) {
			out.active = true
		}
		if w.LastHeartbeat.After(out.lastSeen) {
			out.lastSeen = w.LastHeartbeat
		}
	}
	if reference != nil {
		out.remoteEnabled = objectStoreFromJSON(reference.ObjectStoreJSON).GetEndpoint() != ""
	}
	return out, nil
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
	tasks, total, err := s.store.ListTasks(ctx, space, maxListedTasks, 0)
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
	sort.Strings(rsp.Alerts)
	return rsp, nil
}
