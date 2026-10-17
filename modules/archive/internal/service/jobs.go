package service

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	datasetRefreshEvery = time.Hour
	cleanupEvery        = time.Hour
	vacuumEvery         = 24 * time.Hour
	deletedTaskKeep     = 24 * time.Hour
)

// markDeleted 记下被删除的任务，下次 worker 心跳时通知它清理暂存表。
func (s *Service) markDeleted(space, dataset string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedTasks[space+"/"+dataset] = s.clock()
}

// clearDeleted 在数据集重新创建任务时撤销之前的「已删除」通知，避免 worker 稍后清掉新任务的暂存数据。
func (s *Service) clearDeleted(space, dataset string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deletedTasks, space+"/"+dataset)
}

// deletedTaskRefs 返回仍需通知 worker 的已删除任务，并清理过期条目。
func (s *Service) deletedTaskRefs() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	var out [][2]string
	for key, at := range s.deletedTasks {
		if now.Sub(at) > deletedTaskKeep {
			delete(s.deletedTasks, key)
			continue
		}
		var space, dataset string
		for i := 0; i < len(key); i++ {
			if key[i] == '/' {
				space, dataset = key[:i], key[i+1:]
				break
			}
		}
		out = append(out, [2]string{space, dataset})
	}
	return out
}

// Trigger 是全局触发器（每秒一次）：处理到点的启用任务。
func (s *Service) Trigger(ctx context.Context) error {
	result, problems, err := s.store.TriggerDue(ctx, s.clock(), func(task store.Task, now time.Time) (time.Time, error) {
		plan, err := s.plan(task.ScheduleCron, task.ScheduleTZ)
		if err != nil {
			return time.Time{}, err
		}
		return plan.Next(now), nil
	})
	for _, problem := range problems {
		log.ErrorContextf(ctx, "archive trigger: %v", problem)
	}
	if err != nil {
		return fmt.Errorf("archive trigger: %w", err)
	}
	if result.Created > 0 || result.Skipped > 0 {
		log.InfoContextf(ctx, "archive trigger created=%d skipped=%d", result.Created, result.Skipped)
	}
	return nil
}

// Maintain 是维护定时器（每分钟一次）：回收过期租约；每小时刷新各任务的保留期与 DataNode、清理运行记录；每天回收空闲页。
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
		removed, err := s.store.CleanupRuns(ctx, now, s.cfg.Retention.ScheduledSuccess, s.cfg.Retention.Other)
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

// refreshDatasets 从 Storage 读取各任务数据集的线上保留期与 DataNode。单个数据集失败不影响其他数据集。
func (s *Service) refreshDatasets(ctx context.Context) error {
	tasks, _, err := s.store.ListTasks(ctx, "", 100000, 0)
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
		if err := s.store.UpdateTaskDataset(ctx, task.SpaceID, task.DatasetID, ds.DataNodeID, ds.Retention); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d archive datasets could not be refreshed", failed)
	}
	return nil
}
