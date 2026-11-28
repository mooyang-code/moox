package service

import (
	"context"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/proto"
)

// workerOnline 报告 worker 在 2 分钟内是否有心跳。
func workerOnline(w store.Worker, now time.Time) bool {
	return report.EvaluatePresence(true, w.LastHeartbeat, now, report.DefaultPresenceTimeout) == report.PresenceOnline
}

// Presence 评估归档 worker 在线状态：存在启用的归档任务才要求有 worker，看它是否在 2 分钟内有心跳。
func (s *Service) Presence(ctx context.Context) (state int, workerID string, lastHeartbeat time.Time, err error) {
	required, err := s.store.HasEnabledTasks(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	worker, ok, err := s.store.GetWorker(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	if ok {
		workerID, lastHeartbeat = worker.WorkerID, worker.LastHeartbeat
	}
	return report.EvaluatePresence(required, lastHeartbeat, s.clock(), report.DefaultPresenceTimeout), workerID, lastHeartbeat, nil
}

// DelegatedWorkerReport 是 archive-mgr 代归档 worker 上报给 Monitor 的一行（设计 3.8）：worker 只给管理端发心跳，
// 心跳被接受即算就绪（对象存储是否可用另有检查）；Monitor 按心跳时间判断它是否停止上报。从没心跳过时返回 nil。
// worker 的心跳不带启动标识，所以 Monitor 发现不了它反复重启。
func (s *Service) DelegatedWorkerReport(ctx context.Context) (*metricspb.DelegatedReport, error) {
	worker, ok, err := s.store.GetWorker(ctx)
	if err != nil || !ok || worker.LastHeartbeat.IsZero() {
		return nil, err
	}
	return &metricspb.DelegatedReport{
		ServiceName: "archive-worker", InstanceId: worker.WorkerID, ServiceVersion: worker.Version,
		LastHeartbeatAt: worker.LastHeartbeat.UnixMilli(), Ready: proto.Bool(true),
	}, nil
}

// ObjectStoreState 评估 worker 的对象存储可用状态（见 report.EvaluateObjectStore）。
// worker 没有配置对象存储、或在线状态不新鲜（2 分钟内没有心跳）时 ok 为 false，不上报。
func (s *Service) ObjectStoreState(ctx context.Context) (workerID string, state int, probeError string, ok bool, err error) {
	worker, found, err := s.store.GetWorker(ctx)
	if err != nil || !found {
		return "", 0, "", false, err
	}
	now := s.clock()
	info := objectStoreFromJSON(worker.ObjectStoreJSON)
	if info.GetEndpoint() == "" || !workerOnline(worker, now) {
		return "", 0, "", false, nil
	}
	// 不可用起点由 worker 按 Unix 毫秒上报，0 表示可用。
	return worker.WorkerID, report.EvaluateObjectStore(timeOfMillis(info.GetUnavailableSince()), now), info.GetProbeError(), true, nil
}

// FailingTasks 返回健康等级为异常（HealthError）的归档任务列表（"空间/数据集"，已排序），供关注项上报。
func (s *Service) FailingTasks(ctx context.Context) ([]string, error) {
	tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
	if err != nil {
		return nil, err
	}
	views, err := s.taskViews(ctx, "", tasks)
	if err != nil {
		return nil, err
	}
	var failing []string
	for _, v := range views {
		if v.GetHealth() == HealthError {
			failing = append(failing, v.GetSpaceId()+"/"+v.GetDatasetId())
		}
	}
	sort.Strings(failing)
	return failing, nil
}
