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
