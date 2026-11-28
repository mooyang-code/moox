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
	"trpc.group/trpc-go/trpc-go/log"
)

var _ archivepb.ArchiveMgrService = (*MgrService)(nil)

// MgrService 实现 trpc.moox.archive.ArchiveMgr。
type MgrService struct{ svc *Service }

const (
	defaultPageSize = 50
	maxPageSize     = 200
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

// createBackfill 返回新任务的归档起点：没填时取「现在 − 线上保留期 + 1 天」（首次跟随整段读线上保留期），
// 保留期为 forever 时必须填写；填了的按 validateBackfill 校验。
func createBackfill(value, retention string, now time.Time) (time.Time, error) {
	if strings.TrimSpace(value) != "" {
		return validateBackfill(value, retention, now)
	}
	earliest, ok, err := earliestBackfill(retention, now)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		return time.Time{}, fmt.Errorf("%w：线上保留期为 forever 的数据集必须填写回补起点", ErrInvalidBackfill)
	}
	// 向上取整到秒（与 clampToEarliest 同一口径），保存的值不早于下界。
	start := earliest.Truncate(time.Second)
	if start.Before(earliest) {
		start = start.Add(time.Second)
	}
	return start, nil
}

// earliestBackfill 返回回补起点的下限；保留期为 forever 时返回 false。
func earliestBackfill(retention string, now time.Time) (time.Time, bool, error) {
	limit, forever, err := parseRetention(retention)
	if err != nil || forever {
		return time.Time{}, false, err
	}
	return now.Add(-limit).Add(24 * time.Hour), true, nil
}

// ErrInvalidDataset 表示数据集不能归档（不是时序数据集、状态不是 active 或 ID 为空）。
var ErrInvalidDataset = errors.New("数据集不能归档")

// ErrInvalidBackfill 表示回补起点不合法。
var ErrInvalidBackfill = errors.New("回补起点不合法")

// loadArchivableDataset 读取数据集并确认它是状态为 active 的时序数据集。
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
		return Dataset{}, fmt.Errorf("%w：数据集 %s 状态不是 active（当前 %q）", ErrInvalidDataset, datasetID, ds.Status)
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
	ds.SpaceID = space
	created, err := s.createTaskForDataset(ctx, createTaskInput{
		Dataset:      ds,
		BackfillFrom: req.GetBackfillFrom(),
		DirName:      req.GetDirName(),
		Disabled:     req.GetDisabled(),
		Now:          s.clock(),
	})
	if err != nil {
		var taken *store.DirNameTakenError
		switch {
		case errors.Is(err, errInvalidTaskParam):
			return &archivepb.CreateArchiveTaskRsp{RetInfo: invalid(unwrapCause(err))}, nil
		case errors.Is(err, errResolveDirName):
			return &archivepb.CreateArchiveTaskRsp{RetInfo: inner(unwrapCause(err))}, nil
		case errors.As(err, &taken):
			return &archivepb.CreateArchiveTaskRsp{RetInfo: conflict(err)}, nil
		case errors.Is(err, store.ErrConflict):
			return &archivepb.CreateArchiveTaskRsp{RetInfo: conflict(fmt.Errorf("数据集 %s 已经有归档任务", ds.DatasetID))}, nil
		default:
			return &archivepb.CreateArchiveTaskRsp{RetInfo: fail(err)}, nil
		}
	}
	return &archivepb.CreateArchiveTaskRsp{RetInfo: success(), Task: s.taskToPB(created, ds.Retention)}, nil
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
	case store.StatusEnabled, store.StatusDisabled:
		status = req.GetStatus()
	default:
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: invalid(fmt.Errorf("status %q 无效，只能是 enabled 或 disabled", req.GetStatus()))}, nil
	}
	now := s.clock()
	updated, err := s.store.UpdateTask(ctx, space, req.GetDatasetId(), store.TaskUpdate{Status: status}, now)
	if err != nil {
		return &archivepb.UpdateArchiveTaskRsp{RetInfo: fail(err)}, nil
	}
	retention := ""
	if !updated.SourceMissing {
		if ds, derr := s.datasetInfo(ctx, updated.SpaceID, updated.DatasetID); derr == nil {
			retention = ds.Retention
		}
	}
	return &archivepb.UpdateArchiveTaskRsp{RetInfo: success(), Task: s.taskToPB(updated, retention)}, nil
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
	run, err := s.store.CreateRun(ctx, space, req.GetDatasetId(), store.RunSpec{Kind: store.KindManual}, s.clock())
	if err != nil {
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
	ds, err := s.datasetInfo(ctx, task.SpaceID, task.DatasetID)
	if err != nil {
		return &archivepb.BackfillArchiveRangeRsp{RetInfo: datasetError(err)}, nil
	}
	limit, forever, perr := parseRetention(ds.Retention)
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
	run, err := s.store.CreateRun(ctx, space, req.GetDatasetId(), store.RunSpec{Kind: store.KindRange, RangeStart: start, RangeEnd: end}, now)
	if err != nil {
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
	status, err := m.svc.workerStatus(ctx)
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
	status, err := m.svc.workerStatus(ctx)
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
	rsp := &archivepb.PrepareArchiveTaskRsp{RetInfo: success(), Freq: ds.Freq, PartitionUnit: unit, Retention: ds.Retention}
	if earliest, ok, _ := earliestBackfill(ds.Retention, s.clock()); ok {
		rsp.EarliestBackfillFrom = fmtRFC(earliest)
	}
	if _, err := s.store.GetTask(ctx, space, ds.DatasetID); err == nil {
		rsp.Exists = true
	}
	rsp.Purpose = purposeOf(ds)
	if rsp.SuggestedDirName, err = s.suggestedDirName(ctx, space, ds); err != nil {
		return &archivepb.PrepareArchiveTaskRsp{RetInfo: inner(err)}, nil
	}
	check := rsp.SuggestedDirName
	if requested := strings.TrimSpace(req.GetDirName()); requested != "" {
		if err := validateDirName(requested); err != nil {
			return &archivepb.PrepareArchiveTaskRsp{RetInfo: invalid(err)}, nil
		}
		check = requested
	}
	// 冲突只看其他数据集：已有任务的数据集占用的是它自己的目录名。
	holder, taken, err := s.store.DirNameHolder(ctx, space, rsp.Purpose, check)
	if err != nil {
		return &archivepb.PrepareArchiveTaskRsp{RetInfo: inner(err)}, nil
	}
	rsp.DirNameConflict = taken && holder != ds.DatasetID
	return rsp, nil
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

// taskViews 补齐任务的线上保留期、最近运行、分区汇总与健康等级。
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
	status, err := s.workerStatus(ctx)
	if err != nil {
		return nil, err
	}
	var backlogs map[string]store.RemoteBacklog
	if status.remoteEnabled {
		if backlogs, err = s.store.RemoteBacklogs(ctx, space); err != nil {
			return nil, err
		}
	}
	spaceTZs := map[string]string{}
	for _, task := range tasks {
		if _, ok := spaceTZs[task.SpaceID]; ok {
			continue
		}
		tz, err := s.spaceTimezone(ctx, task.SpaceID)
		if err != nil {
			log.WarnContextf(ctx, "archive taskViews: resolve space %s timezone: %v", task.SpaceID, err)
			tz = "UTC"
		}
		spaceTZs[task.SpaceID] = tz
	}
	out := make([]*archivepb.ArchiveTask, 0, len(tasks))
	for _, task := range tasks {
		key := task.SpaceID + "/" + task.DatasetID
		retention := ""
		if !task.SourceMissing {
			if ds, derr := s.datasetInfo(ctx, task.SpaceID, task.DatasetID); derr != nil {
				log.WarnContextf(ctx, "archive taskViews: resolve dataset %s metadata: %v", key, derr)
			} else {
				retention = ds.Retention
			}
		}
		view := s.taskToPB(task, retention)
		dayClose, _ := spaceMarket(task.SpaceID)
		in := healthInput{Now: now, Task: task, Retention: retention, OpenParts: openByTask[key], OldestQueued: queued[key], WorkerLastSeen: status.lastSeen,
			Lateness: s.cfg.AllowedLateness, SpaceTZ: spaceTZs[task.SpaceID], SpaceLocalClose: dayClose,
			RemoteEnabled: status.remoteEnabled, Remote: backlogs[key]}
		if run, ok := lastRuns[key]; ok {
			copied := run
			in.LastRun = &copied
			view.LastRun = runToPB(run)
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
	// lastSeen：worker 最近一次心跳的时间，从未心跳时为零值。
	lastSeen time.Time
	// remoteEnabled：worker 最近一次心跳报告启用了对象存储；从未心跳时视为未启用。
	remoteEnabled bool
}

// workerStatus 汇总 worker 的状态。
func (s *Service) workerStatus(ctx context.Context) (workerState, error) {
	w, ok, err := s.store.GetWorker(ctx)
	if err != nil || !ok {
		return workerState{}, err
	}
	return workerState{lastSeen: w.LastHeartbeat, remoteEnabled: objectStoreFromJSON(w.ObjectStoreJSON).GetEndpoint() != ""}, nil
}

func (m *MgrService) GetArchiveOverview(ctx context.Context, _ *archivepb.GetArchiveOverviewReq) (*archivepb.GetArchiveOverviewRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, "", false)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: invalid(err)}, nil
	}
	now := s.clock()
	rsp := &archivepb.GetArchiveOverviewRsp{RetInfo: success()}
	worker, ok, err := s.store.GetWorker(ctx)
	if err != nil {
		return &archivepb.GetArchiveOverviewRsp{RetInfo: inner(err)}, nil
	}
	if ok {
		rsp.Worker = workerToPB(worker, now)
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
	if rsp.EnabledTaskCount > 0 && !rsp.GetWorker().GetActive() {
		rsp.Alerts = append(rsp.Alerts, "归档 worker 不在线")
	}
	sort.Strings(rsp.Alerts)
	return rsp, nil
}

// onlineWorker 返回在线的 worker；不在线时报错。
func (s *Service) onlineWorker(ctx context.Context) (store.Worker, error) {
	w, ok, err := s.store.GetWorker(ctx)
	if err != nil {
		return store.Worker{}, err
	}
	if !ok || !workerOnline(w, s.clock()) {
		return store.Worker{}, errors.New("归档 worker 不在线，无法导入")
	}
	return w, nil
}

// StartArchiveImport 为 moox-cli import 的归档目标建一条导入运行：任务存在、worker 在线才建；
// 任务有排队中或运行中的整段重读、补拉区间或导入时返回冲突，并写明挡住它的运行。
func (m *MgrService) StartArchiveImport(ctx context.Context, req *archivepb.StartArchiveImportReq) (*archivepb.StartArchiveImportRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.StartArchiveImportRsp{RetInfo: invalid(err)}, nil
	}
	if _, err := s.store.GetTask(ctx, space, req.GetDatasetId()); err != nil {
		return &archivepb.StartArchiveImportRsp{RetInfo: fail(err)}, nil
	}
	worker, err := s.onlineWorker(ctx)
	if err != nil {
		return &archivepb.StartArchiveImportRsp{RetInfo: conflict(err)}, nil
	}
	run, err := s.store.CreateRun(ctx, space, req.GetDatasetId(), store.RunSpec{Kind: store.KindImport}, s.clock())
	if err != nil {
		return &archivepb.StartArchiveImportRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.StartArchiveImportRsp{RetInfo: success(), Run: runToPB(run), WorkerId: worker.WorkerID}, nil
}

func (m *MgrService) ClearArchiveUnrecoverable(ctx context.Context, req *archivepb.ClearArchiveUnrecoverableReq) (*archivepb.ClearArchiveUnrecoverableRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: invalid(err)}, nil
	}
	rawStart, rawEnd := strings.TrimSpace(req.GetStart()), strings.TrimSpace(req.GetEnd())
	var start, end time.Time
	if rawStart != "" || rawEnd != "" {
		if rawStart == "" || rawEnd == "" {
			return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: invalid(errors.New("start 与 end 必须同时填写（都不填表示清除全部）"))}, nil
		}
		start, err = parseRFC("start", rawStart)
		if err == nil {
			end, err = parseRFC("end", rawEnd)
		}
		if err == nil && !start.Before(end) {
			err = errors.New("start 必须早于 end")
		}
		if err != nil {
			return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: invalid(err)}, nil
		}
	}
	removed, err := s.store.ClearUnrecoverable(ctx, space, req.GetDatasetId(), start, end)
	if err != nil {
		return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: fail(err)}, nil
	}
	rsp := &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: success()}
	for _, item := range removed {
		rsp.Removed = append(rsp.Removed, &archivepb.UnrecoverableRange{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag, StartTime: item.StartTime, EndTime: item.EndTime})
	}
	return rsp, nil
}
