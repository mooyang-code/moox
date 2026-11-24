package service

import (
	"context"
	"fmt"
	"time"

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

// Maintain 是维护定时器（每分钟一次）：回收过期租约；每小时刷新各任务的保留期与数据集元数据、清理运行记录；每天回收空闲页。
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

// refreshDatasets 从 Storage 读取各任务数据集的线上保留期与元数据。单个数据集失败不影响其他数据集。
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
		if err := s.store.UpdateTaskDataset(ctx, task.SpaceID, task.DatasetID, ds.Retention, metaJSON(ds)); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d archive datasets could not be refreshed", failed)
	}
	return nil
}
