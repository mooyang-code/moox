package service

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	datasetRefreshEvery = time.Hour
	cleanupEvery        = time.Hour
	vacuumEvery         = 24 * time.Hour
	// tickSuccessRetention 是成功的到点拉取运行的保留期（每天最多 1440 条，只留 24 小时）。
	tickSuccessRetention = 24 * time.Hour
)

// Trigger 是全局检查（每分钟第 25 秒）：对每个启用的任务按拉取边界与复核边界判定，到点才生成运行，见 schedule.Decide。
func (s *Service) Trigger(ctx context.Context) error {
	now := s.clock()
	result, problems, err := s.store.TriggerDue(ctx, now, func(task store.Task) (store.TriggerPlan, error) {
		decision, err := schedule.Decide(s.timing(task.SpaceID, task.Freq, task.PullEveryBars), task.LastBoundary, task.LastReview, now)
		if err != nil {
			return store.TriggerPlan{}, err
		}
		plan := store.TriggerPlan{RangeStart: decision.RangeStart, RangeEnd: decision.RangeEnd, LastBoundary: decision.LastBoundary, LastReview: decision.LastReview}
		switch decision.Action {
		case schedule.ActionReview:
			plan.Kind = store.KindScheduled
		case schedule.ActionTick:
			plan.Kind = store.KindTick
		}
		return plan, nil
	})
	for _, problem := range problems {
		log.ErrorContextf(ctx, "archive trigger: %v", problem)
	}
	if err != nil {
		return fmt.Errorf("archive trigger: %w", err)
	}
	if result.Reviews > 0 || result.Ticks > 0 || result.Skipped > 0 {
		log.InfoContextf(ctx, "archive trigger reviews=%d ticks=%d skipped=%d", result.Reviews, result.Ticks, result.Skipped)
	}
	return nil
}

// Maintain 是维护定时器（每分钟一次）：回收过期租约；每小时刷新各任务的保留期与 DataNode、清理运行记录；每天回收空闲页；
// 最后在距上次成功满 10 分钟时刷新避让规则（读取各空间的采集频率，合计不超过 20 秒）。
func (s *Service) Maintain(ctx context.Context) error {
	now := s.clock()
	expired, err := s.store.ExpireLeases(ctx, now)
	if err != nil {
		return fmt.Errorf("expire archive leases: %w", err)
	}
	if expired > 0 {
		log.WarnContextf(ctx, "archive maintain: %d runs lost their worker", expired)
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
	if doCleanup {
		removed, err := s.store.CleanupRuns(ctx, now, tickSuccessRetention, s.cfg.Retention.ScheduledSuccess, s.cfg.Retention.Other)
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
	if err := s.maybeRefreshAvoidance(ctx, now); err != nil {
		log.WarnContextf(ctx, "archive maintain: refresh avoidance: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// refreshDatasets 从 Storage 读取各任务数据集的线上保留期与 DataNode。单个数据集失败不影响其他数据集。
func (s *Service) refreshDatasets(ctx context.Context) error {
	tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
	if err != nil {
		return err
	}
	var failed int
	for _, task := range tasks {
		ds, err := s.datasets.GetDataset(ctx, task.SpaceID, task.DatasetID)
		if err != nil {
			failed++
			log.WarnContextf(ctx, "archive maintain: refresh dataset %s/%s: %v", task.SpaceID, task.DatasetID, err)
			continue
		}
		if err := s.store.UpdateTaskDataset(ctx, task.SpaceID, task.DatasetID, ds.DataNodeID, ds.Retention, metaJSON(ds)); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d archive datasets could not be refreshed", failed)
	}
	return nil
}
