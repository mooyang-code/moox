package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

var _ archivepb.ArchiveWorkerService = (*WorkerService)(nil)

// WorkerService 实现 trpc.moox.archive.ArchiveWorker，只有外部身份 archive-worker 可以调用。
type WorkerService struct{ svc *Service }

func (w *WorkerService) ArchiveWorkerHeartbeat(ctx context.Context, req *archivepb.ArchiveWorkerHeartbeatReq) (*archivepb.ArchiveWorkerHeartbeatRsp, error) {
	s := w.svc
	if strings.TrimSpace(req.GetWorkerId()) == "" || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: invalid(errors.New("worker_id 与 instance_id 不能为空"))}, nil
	}
	now := s.clock()
	ttl := s.cfg.Worker.TakeoverWait
	result, err := s.store.Heartbeat(ctx, store.Worker{
		WorkerID: req.GetWorkerId(), InstanceID: req.GetInstanceId(), Version: req.GetVersion(), RootDir: req.GetRootDir(),
		DiskTotalBytes: req.GetDiskTotalBytes(), DiskFreeBytes: req.GetDiskFreeBytes(), StagingBytes: req.GetStagingBytes(),
		Maintaining: req.GetMaintaining(), PacingJSON: req.GetPacingJson(), RunningRunIDs: req.GetRunningRunIds(),
	}, now, ttl)
	if err != nil {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: inner(err)}, nil
	}
	if result.Conflict {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: conflict(fmt.Errorf("worker_id %q 已有另一个存活的实例，请先停止它", req.GetWorkerId()))}, nil
	}
	rsp := &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: success(), Active: result.Active, LeaseTtlSeconds: uint32(ttl / time.Second)}
	canceled, err := s.store.RunningRunIDsCanceled(ctx, req.GetWorkerId(), req.GetRunningRunIds())
	if err != nil {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: inner(err)}, nil
	}
	rsp.CancelRunIds = canceled
	if result.Active {
		for _, ref := range s.deletedTaskRefs() {
			rsp.DeletedTasks = append(rsp.DeletedTasks, &archivepb.TaskRef{SpaceId: ref[0], DatasetId: ref[1]})
		}
	}
	return rsp, nil
}

func (w *WorkerService) PullArchiveRun(ctx context.Context, req *archivepb.PullArchiveRunReq) (*archivepb.PullArchiveRunRsp, error) {
	s := w.svc
	if strings.TrimSpace(req.GetWorkerId()) == "" || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &archivepb.PullArchiveRunRsp{RetInfo: invalid(errors.New("worker_id 与 instance_id 不能为空"))}, nil
	}
	now := s.clock()
	active, err := s.store.IsActiveWorker(ctx, req.GetWorkerId(), req.GetInstanceId(), now)
	if err != nil {
		return &archivepb.PullArchiveRunRsp{RetInfo: inner(err)}, nil
	}
	if !active {
		// 备用 worker 或租约已过期的实例什么都领不到，等心跳重新取得租约。
		return &archivepb.PullArchiveRunRsp{RetInfo: success()}, nil
	}
	busy := map[string]int{}
	for _, load := range req.GetBusyDataNodes() {
		busy[load.GetDataNodeId()] += int(load.GetRunning())
	}
	claimed, err := s.store.ClaimRuns(ctx, store.ClaimRequest{
		WorkerID: req.GetWorkerId(), FreeSlots: int(req.GetFreeSlots()), MaxPerDataNode: int(req.GetMaxRunsPerDataNode()),
		BusyByDataNode: busy, Now: now, LeaseTTL: s.cfg.Run.LeaseTTL,
	})
	if err != nil {
		return &archivepb.PullArchiveRunRsp{RetInfo: inner(err)}, nil
	}
	rsp := &archivepb.PullArchiveRunRsp{RetInfo: success()}
	for _, c := range claimed {
		rsp.Runs = append(rsp.Runs, s.assignment(c))
	}
	return rsp, nil
}

// assignment 把领取到的运行与任务打包成下发给 worker 的参数。
func (s *Service) assignment(c store.Claimed) *archivepb.RunAssignment {
	spaceCfg := s.cfg.Space(c.Task.SpaceID)
	return &archivepb.RunAssignment{
		RunId: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Kind: c.Run.Kind, SpaceId: c.Task.SpaceID, DatasetId: c.Task.DatasetID,
		Freq: c.Task.Freq, PartitionUnit: c.Task.PartitionUnit, DataNodeId: c.Task.DataNodeID, Retention: c.Task.Retention,
		IntervalSeconds: uint32(c.Task.IntervalSeconds), MinLookbackSeconds: uint32(s.cfg.Schedule.MinLookback / time.Second),
		SpaceTz: spaceCfg.TZ, LocalClose: spaceCfg.LocalClose, StagingRetentionSeconds: uint32(s.cfg.StagingRetention / time.Second),
		ScheduledAt: fmtRFC(c.Run.ScheduledAt), StartedAt: fmtRFC(c.Run.StartedAt), RangeStart: fmtRFC(c.Run.RangeStart), RangeEnd: fmtRFC(c.Run.RangeEnd),
		BackfillFrom: fmtRFC(c.Task.BackfillFrom), LeaseTtlSeconds: uint32(s.cfg.Run.LeaseTTL / time.Second),
	}
}

func (w *WorkerService) ReportArchiveRunProgress(ctx context.Context, req *archivepb.ReportArchiveRunProgressReq) (*archivepb.ReportArchiveRunProgressRsp, error) {
	s := w.svc
	if req.GetRunId() == "" || req.GetLeaseToken() == "" || req.GetWorkerId() == "" {
		return &archivepb.ReportArchiveRunProgressRsp{RetInfo: invalid(errors.New("run_id、lease_token、worker_id 不能为空"))}, nil
	}
	canceled, err := s.store.Progress(ctx, req.GetRunId(), req.GetWorkerId(), req.GetLeaseToken(), store.RunStats{
		SubjectsTotal: req.GetSubjectsTotal(), SubjectsDone: req.GetSubjectsDone(), RequestCount: req.GetRequestCount(),
		BytesRead: req.GetBytesRead(), RowsRead: req.GetRowsRead(),
	}, s.clock(), s.cfg.Run.LeaseTTL)
	if err != nil {
		return &archivepb.ReportArchiveRunProgressRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.ReportArchiveRunProgressRsp{RetInfo: success(), Canceled: canceled}, nil
}

func (w *WorkerService) CompleteArchiveRun(ctx context.Context, req *archivepb.CompleteArchiveRunReq) (*archivepb.CompleteArchiveRunRsp, error) {
	s := w.svc
	if req.GetRunId() == "" || req.GetLeaseToken() == "" || req.GetWorkerId() == "" {
		return &archivepb.CompleteArchiveRunRsp{RetInfo: invalid(errors.New("run_id、lease_token、worker_id 不能为空"))}, nil
	}
	switch req.GetStatus() {
	case store.RunSucceeded, store.RunFailed, store.RunCanceled:
	default:
		return &archivepb.CompleteArchiveRunRsp{RetInfo: invalid(fmt.Errorf("status %q 无效", req.GetStatus()))}, nil
	}
	finalUntil, err := parseRFC("final_until", req.GetFinalUntil())
	if err != nil {
		return &archivepb.CompleteArchiveRunRsp{RetInfo: invalid(err)}, nil
	}
	partitions, err := partitionsFromPB(req.GetPartitions())
	if err != nil {
		return &archivepb.CompleteArchiveRunRsp{RetInfo: invalid(err)}, nil
	}
	unrecoverable := make([]store.Unrecoverable, 0, len(req.GetUnrecoverable()))
	for _, item := range req.GetUnrecoverable() {
		unrecoverable = append(unrecoverable, store.Unrecoverable{SubjectID: item.GetSubjectId(), SeriesTag: item.GetSeriesTag(), StartTime: item.GetStartTime(), EndTime: item.GetEndTime()})
	}
	err = s.store.CompleteRun(ctx, store.CompleteRequest{
		RunID: req.GetRunId(), WorkerID: req.GetWorkerId(), LeaseToken: req.GetLeaseToken(), Status: req.GetStatus(), Message: req.GetMessage(),
		Stats: store.RunStats{
			SubjectsTotal: req.GetSubjectsTotal(), SubjectsDone: req.GetSubjectsDone(), RequestCount: req.GetRequestCount(), BytesRead: req.GetBytesRead(),
			RowsRead: req.GetRowsRead(), RowsAdded: req.GetRowsAdded(), RowsChanged: req.GetRowsChanged(),
			PartitionsExported: req.GetPartitionsExported(), PartitionsFinalized: req.GetPartitionsFinalized(),
		},
		Unrecoverable: unrecoverable, Partitions: partitions, FinalUntil: finalUntil, Now: s.clock(),
	})
	if err != nil {
		return &archivepb.CompleteArchiveRunRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.CompleteArchiveRunRsp{RetInfo: success()}, nil
}

func partitionsFromPB(items []*archivepb.PartitionReport) ([]store.Partition, error) {
	out := make([]store.Partition, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		key := strings.TrimSpace(item.GetPartitionKey())
		if key == "" {
			return nil, errors.New("partition_key 不能为空")
		}
		if seen[key] {
			return nil, fmt.Errorf("分区 %s 在同一次上报里重复", key)
		}
		seen[key] = true
		switch item.GetStatus() {
		case store.PartitionOpen, store.PartitionExported, store.PartitionFinal:
		default:
			return nil, fmt.Errorf("分区 %s 的状态 %q 无效", key, item.GetStatus())
		}
		p := store.Partition{
			PartitionKey: key, Status: item.GetStatus(), RowCount: item.GetRowCount(), SubjectCount: item.GetSubjectCount(), FilePath: item.GetFilePath(),
			FileBytes: item.GetFileBytes(), FileSHA256: item.GetFileSha256(), Columns: append([]string(nil), item.GetColumns()...), Preview: item.GetPreview(),
			ExportCount: item.GetExportCount(),
		}
		var err error
		for _, field := range []struct {
			name, value string
			dest        *time.Time
		}{
			{"range_start", item.GetRangeStart(), &p.RangeStart}, {"range_end", item.GetRangeEnd(), &p.RangeEnd},
			{"min_data_time", item.GetMinDataTime(), &p.MinDataTime}, {"max_data_time", item.GetMaxDataTime(), &p.MaxDataTime},
			{"last_changed_at", item.GetLastChangedAt(), &p.LastChangedAt}, {"exported_at", item.GetExportedAt(), &p.ExportedAt},
			{"finalized_at", item.GetFinalizedAt(), &p.FinalizedAt},
		} {
			if *field.dest, err = parseRFC(fmt.Sprintf("分区 %s 的 %s", key, field.name), field.value); err != nil {
				return nil, err
			}
		}
		if p.RangeStart.IsZero() || !p.RangeStart.Before(p.RangeEnd) {
			return nil, fmt.Errorf("分区 %s 的 range_start 必须早于 range_end", key)
		}
		if p.Status == store.PartitionFinal && p.FinalizedAt.IsZero() {
			return nil, fmt.Errorf("分区 %s 已定稿但没有 finalized_at", key)
		}
		out = append(out, p)
	}
	return out, nil
}
