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
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	datasetRefreshEvery = time.Hour
	cleanupEvery        = time.Hour
	vacuumEvery         = 24 * time.Hour
	// followSuccessRetention 是成功的 follow 运行的保留期（每任务每天最多 1440 条，只留 24 小时）。
	followSuccessRetention = 24 * time.Hour
)

// Trigger 是全局检查（每分钟第 25 秒）：给每个启用、且没有排队中或运行中运行的任务生成一条 follow 运行。
// 不按整点窗口跳过：避让只由 worker 的 pacing 执行，窗口内的 follow 只做导出与定稿。
func (s *Service) Trigger(ctx context.Context) error {
	created, err := s.store.CreateFollowRuns(ctx, s.clock())
	if err != nil {
		return fmt.Errorf("archive trigger: %w", err)
	}
	if created > 0 {
		log.DebugContextf(ctx, "archive trigger follow=%d", created)
	}
	return nil
}

// Maintain 是维护定时器（每分钟一次）：回收过期租约；为符合条件的数据集自动补齐归档任务；
// 每小时刷新各任务的保留期与数据集元数据、清理运行记录；每天回收空闲页。
func (s *Service) Maintain(ctx context.Context) error {
	now := s.clock()
	worker, ok, err := s.store.GetWorker(ctx)
	if err != nil {
		return fmt.Errorf("读取归档 worker 状态失败: %w", err)
	}
	online := ok && workerOnline(worker, now)
	expired, err := s.store.ExpireLeases(ctx, now, online)
	if err != nil {
		return fmt.Errorf("回收过期的运行租约失败: %w", err)
	}
	if expired > 0 {
		log.WarnContextf(ctx, "归档维护：%d 个运行的租约已过期，记为失败（worker 在线：%v）", expired, online)
	}
	s.mu.Lock()
	doRefresh := now.Sub(s.lastRefresh) >= datasetRefreshEvery
	doCleanup := now.Sub(s.lastCleanup) >= cleanupEvery
	doVacuum := now.Sub(s.lastVacuum) >= vacuumEvery
	s.mu.Unlock()
	var firstErr error
	if doRefresh {
		if err := s.refreshDatasets(ctx); err != nil {
			firstErr = err
		} else {
			s.mu.Lock()
			s.lastRefresh = now
			s.mu.Unlock()
		}
	}
	if err := s.ensureTasks(ctx, now); err != nil && firstErr == nil {
		firstErr = err
	}
	if doCleanup {
		removed, err := s.store.CleanupRuns(ctx, now, followSuccessRetention, s.cfg.Retention.Other)
		if err != nil {
			firstErr = err
		} else {
			s.mu.Lock()
			s.lastCleanup = now
			s.mu.Unlock()
			if removed > 0 {
				log.InfoContextf(ctx, "archive maintain: removed %d old runs", removed)
			}
		}
	}
	if doVacuum {
		if err := s.store.IncrementalVacuum(ctx); err != nil {
			firstErr = err
		} else {
			s.mu.Lock()
			s.lastVacuum = now
			s.mu.Unlock()
		}
	}
	return firstErr
}

// refreshDatasets 从 Storage 读取各任务数据集的线上保留期与元数据。单个数据集失败不影响其他数据集；
// 仅在 Storage 明确返回“不存在”时在任务上记“源数据集不存在”并停止生成跟随（不停用任务，也不触发每分钟重试），
// 后续刷新发现数据集重新出现时自动清掉标记继续归档。网络或存储故障不动任务状态，照常下一分钟重试。
func (s *Service) refreshDatasets(ctx context.Context) error {
	tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
	if err != nil {
		return err
	}
	now := s.clock()
	var failed int
	for _, task := range tasks {
		ds, err := s.datasets.GetDataset(ctx, task.SpaceID, task.DatasetID)
		if err != nil {
			if errors.Is(err, ErrDatasetNotFound) {
				if markErr := s.store.MarkTaskSourceMissing(ctx, task.SpaceID, task.DatasetID, now); markErr != nil {
					return markErr
				}
				if !task.SourceMissing {
					log.WarnContextf(ctx, "归档维护：数据集 %s/%s 在 Storage 中不存在，已标记并暂停生成跟随运行", task.SpaceID, task.DatasetID)
				}
				continue
			}
			failed++
			log.WarnContextf(ctx, "archive maintain: refresh dataset %s/%s: %v", task.SpaceID, task.DatasetID, err)
			continue
		}
		if err := s.store.UpdateTaskDataset(ctx, task.SpaceID, task.DatasetID, ds.Retention, metaJSON(ds)); err != nil {
			return err
		}
		if task.SourceMissing {
			log.InfoContextf(ctx, "归档维护：数据集 %s/%s 已重新出现，恢复生成跟随运行", task.SpaceID, task.DatasetID)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d archive datasets could not be refreshed", failed)
	}
	return nil
}

// isAutoArchivable 报告数据集是否应自动建立归档任务：已激活（active）、时序类型（time_series）、有限保留期（非空且非 forever）。
func isAutoArchivable(ds Dataset) bool {
	if strings.TrimSpace(ds.SpaceID) == "" || strings.TrimSpace(ds.DatasetID) == "" {
		return false
	}
	if !strings.EqualFold(ds.DataKind, timeSeriesKind) || !strings.EqualFold(ds.Status, datasetActive) {
		return false
	}
	retention := strings.TrimSpace(ds.Retention)
	return retention != "" && !strings.EqualFold(retention, "forever")
}

// EnsureIssues 返回最近一次自动补齐归档任务未能处理的问题列表（供指标上报为关注项）；全部补齐或尚未失败时为空。
func (s *Service) EnsureIssues() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ensureIssues) == 0 {
		return nil
	}
	out := make([]string, len(s.ensureIssues))
	copy(out, s.ensureIssues)
	return out
}

func (s *Service) setEnsureIssues(issues []string) {
	s.mu.Lock()
	s.ensureIssues = issues
	s.mu.Unlock()
}

// listDatasetsFailAlertThreshold 是读取 Storage 数据集清单连续失败多少轮后才上报关注项，
// 避免 Storage 短暂抖动或重启时的单次失败立刻触发 critical 告警。
const listDatasetsFailAlertThreshold = 3

var (
	errInvalidTaskParam = errors.New("invalid archive task param")
	errResolveDirName   = errors.New("resolve archive dir name")
)

type createTaskInput struct {
	Dataset      Dataset
	BackfillFrom string
	DirName      string
	Paused       bool
	Creator      string
	Now          time.Time
}

// createTaskForDataset 统一校验并创建归档任务，供手工 CreateArchiveTask 与定时自动补齐共用。
func (s *Service) createTaskForDataset(ctx context.Context, in createTaskInput) (store.Task, error) {
	ds := in.Dataset
	unit, err := schedule.PartitionUnit(ds.Freq)
	if err != nil {
		return store.Task{}, fmt.Errorf("%w: %w", errInvalidTaskParam, err)
	}
	backfill, err := createBackfill(in.BackfillFrom, ds.Retention, in.Now)
	if err != nil {
		return store.Task{}, fmt.Errorf("%w: %w", errInvalidTaskParam, err)
	}
	dirName := strings.TrimSpace(in.DirName)
	if dirName == "" {
		if dirName, err = s.suggestedDirName(ctx, ds.SpaceID, ds); err != nil {
			return store.Task{}, fmt.Errorf("%w: %w", errResolveDirName, err)
		}
	}
	if err := validateDirName(dirName); err != nil {
		return store.Task{}, fmt.Errorf("%w: %w", errInvalidTaskParam, err)
	}
	status := store.StatusEnabled
	if in.Paused {
		status = store.StatusPaused
	}
	task := store.Task{
		MetaJSON: metaJSON(ds), Purpose: purposeOf(ds), DirName: dirName,
		SpaceID: ds.SpaceID, DatasetID: ds.DatasetID, Freq: ds.Freq, PartitionUnit: unit, Retention: ds.Retention,
		BackfillFrom: backfill, Status: status, Creator: in.Creator,
	}
	return s.store.CreateTask(ctx, task)
}

// ensureTasks 从 Storage 读取全部数据集，为符合条件但还没有归档任务的数据集按默认参数补齐任务。
// 已有任务（含人工停用的任务）一律跳过，不会重建也不会改回启用；手工删除的任务会被重新建出。
// 读 Storage 数据集清单需连续失败达到阈值才上报关注项，单次瞬时失败不误报；个别数据集无法建出任务时记录到 ensureIssues。
func (s *Service) ensureTasks(ctx context.Context, now time.Time) error {
	tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
	if err != nil {
		return fmt.Errorf("列出已有归档任务失败: %w", err)
	}
	existing := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		existing[task.SpaceID+"/"+task.DatasetID] = struct{}{}
	}
	datasets, err := s.datasets.ListDatasets(ctx)
	if err != nil {
		s.mu.Lock()
		s.listDatasetsFailCount++
		fails := s.listDatasetsFailCount
		if fails >= listDatasetsFailAlertThreshold {
			s.ensureIssues = []string{"读取 Storage 数据集清单失败（详见 archive-mgr 日志）"}
		}
		s.mu.Unlock()
		log.WarnContextf(ctx, "归档维护：自动补齐读取 Storage 数据集清单失败（连续第 %d 次）: %v", fails, err)
		return nil
	}
	s.mu.Lock()
	s.listDatasetsFailCount = 0
	s.mu.Unlock()

	candidates := make([]Dataset, 0, len(datasets))
	seen := make(map[string]struct{}, len(datasets))
	for _, ds := range datasets {
		if !isAutoArchivable(ds) {
			continue
		}
		ds.SpaceID = strings.TrimSpace(ds.SpaceID)
		ds.DatasetID = strings.TrimSpace(ds.DatasetID)
		key := ds.SpaceID + "/" + ds.DatasetID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, has := existing[key]; has {
			continue
		}
		candidates = append(candidates, ds)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].SpaceID+"/"+candidates[i].DatasetID < candidates[j].SpaceID+"/"+candidates[j].DatasetID
	})
	var issues []string
	for _, ds := range candidates {
		if issue := s.ensureDatasetTask(ctx, ds, now); issue != "" {
			issues = append(issues, issue)
		}
	}
	s.setEnsureIssues(issues)
	return nil
}

// ensureDatasetTask 为单个符合条件的数据集按默认参数创建归档任务；成功或并发已创建时返回空串，失败返回稳定的关注项描述。
func (s *Service) ensureDatasetTask(ctx context.Context, ds Dataset, now time.Time) string {
	key := ds.SpaceID + "/" + ds.DatasetID
	created, err := s.createTaskForDataset(ctx, createTaskInput{
		Dataset: ds,
		Creator: "archive-mgr",
		Now:     now,
	})
	if err != nil {
		var taken *store.DirNameTakenError
		switch {
		case errors.As(err, &taken):
			log.WarnContextf(ctx, "归档维护：自动补齐 %s 失败: %v", key, err)
			return fmt.Sprintf("%s：目录名 %s 已被 %s 使用", key, taken.DirName, taken.DatasetID)
		case errors.Is(err, store.ErrConflict):
			return ""
		case errors.Is(err, errResolveDirName):
			log.WarnContextf(ctx, "归档维护：自动补齐 %s 解析建议目录名失败: %v", key, err)
			return fmt.Sprintf("%s：读取源数据集失败（详见 archive-mgr 日志）", key)
		case errors.Is(err, errInvalidTaskParam):
			cause := unwrapCause(err)
			log.WarnContextf(ctx, "归档维护：自动补齐 %s 失败: %v", key, cause)
			return fmt.Sprintf("%s：%s", key, cause.Error())
		default:
			log.WarnContextf(ctx, "归档维护：自动补齐 %s 写库失败: %v", key, err)
			return fmt.Sprintf("%s：创建归档任务失败（详见 archive-mgr 日志）", key)
		}
	}
	log.InfoContextf(ctx, "归档维护：已自动补齐归档任务 %s（目录 %s/%s）", key, created.Purpose, created.DirName)
	return ""
}

// unwrapCause 去掉外层分类哨兵前缀，保留面向调用方或关注项的原始中文错误说明。
func unwrapCause(err error) error {
	type multiUnwrap interface{ Unwrap() []error }
	if multi, ok := err.(multiUnwrap); ok {
		parts := multi.Unwrap()
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return err
}
