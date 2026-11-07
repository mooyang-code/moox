package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/report"
)

// Presence 评估归档 worker 在线状态：存在启用的归档任务才要求有 worker；按当前单活动 worker 的模型，
// 只看最近一次心跳的那台是否在 2 分钟内有心跳，换机后留下的旧登记不影响判定。
func (s *Service) Presence(ctx context.Context) (state int, workerID string, lastHeartbeat time.Time, err error) {
	required, err := s.store.HasEnabledTasks(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	worker, ok, err := s.store.LatestWorker(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	if ok {
		workerID, lastHeartbeat = worker.WorkerID, worker.LastHeartbeat
	}
	return report.EvaluatePresence(required, lastHeartbeat, s.clock(), report.DefaultPresenceTimeout), workerID, lastHeartbeat, nil
}

// TaskHealthLevels 返回全部任务的健康等级，供指标上报（Monitor 据此告警）。
func (s *Service) TaskHealthLevels(ctx context.Context) ([]report.ArchiveTaskLevel, error) {
	tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
	if err != nil {
		return nil, err
	}
	views, err := s.taskViews(ctx, "", tasks)
	if err != nil {
		return nil, err
	}
	out := make([]report.ArchiveTaskLevel, 0, len(views))
	for _, v := range views {
		level := report.ArchiveTaskHealthOK
		switch v.GetHealth() {
		case HealthWarning:
			level = report.ArchiveTaskHealthWarning
		case HealthError:
			level = report.ArchiveTaskHealthError
		case HealthPaused:
			level = report.ArchiveTaskHealthPaused
		}
		out = append(out, report.ArchiveTaskLevel{Space: v.GetSpaceId(), Dataset: v.GetDatasetId(), Level: level})
	}
	return out, nil
}

// RetireArchiveWorker 删除一条不再使用的 worker 登记；仍持有活动租约时拒绝。
func (m *MgrService) RetireArchiveWorker(ctx context.Context, req *archivepb.RetireArchiveWorkerReq) (*archivepb.RetireArchiveWorkerRsp, error) {
	workerID := strings.TrimSpace(req.GetWorkerId())
	if workerID == "" {
		return &archivepb.RetireArchiveWorkerRsp{RetInfo: invalid(errors.New("worker_id 不能为空"))}, nil
	}
	retired, err := m.svc.store.RetireWorker(ctx, workerID, m.svc.clock())
	if errors.Is(err, store.ErrWorkerActive) {
		return &archivepb.RetireArchiveWorkerRsp{RetInfo: conflict(errors.New("worker 仍持有活动租约，停止它约 1 分钟后再退役"))}, nil
	}
	if err != nil {
		return &archivepb.RetireArchiveWorkerRsp{RetInfo: inner(err)}, nil
	}
	return &archivepb.RetireArchiveWorkerRsp{RetInfo: success(), Retired: retired}, nil
}
