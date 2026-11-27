package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"trpc.group/trpc-go/trpc-go/log"
)

var _ archivepb.ArchiveWorkerService = (*WorkerService)(nil)

// WorkerService 实现 trpc.moox.archive.ArchiveWorker，只有调用方 archive-worker（普通组件，用所在主机的密钥签名）可以调用。
type WorkerService struct{ svc *Service }

func (w *WorkerService) ArchiveWorkerHeartbeat(ctx context.Context, req *archivepb.ArchiveWorkerHeartbeatReq) (*archivepb.ArchiveWorkerHeartbeatRsp, error) {
	s := w.svc
	if strings.TrimSpace(req.GetWorkerId()) == "" {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: invalid(errors.New("worker_id 不能为空"))}, nil
	}
	err := s.store.Heartbeat(ctx, store.Worker{
		WorkerID: req.GetWorkerId(), Version: req.GetVersion(), RootDir: req.GetRootDir(),
		DiskTotalBytes: req.GetDiskTotalBytes(), DiskFreeBytes: req.GetDiskFreeBytes(), DiskUsedBytes: req.GetDiskUsedBytes(), MinFreeBytes: req.GetMinFreeBytes(),
		StagingBytes: req.GetStagingBytes(), Maintaining: req.GetMaintaining(), PacingJSON: req.GetPacingJson(), RunningRunIDs: req.GetRunningRunIds(),
		ObjectStoreJSON: objectStoreToJSON(req.GetObjectStore()),
	}, s.clock())
	if err != nil {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: inner(err)}, nil
	}
	rsp := &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: success()}
	canceled, err := s.store.RunningRunIDsCanceled(ctx, req.GetRunningRunIds())
	if err != nil {
		return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: inner(err)}, nil
	}
	rsp.CancelRunIds = canceled
	if req.GetIncludeTasks() {
		tasks, _, err := s.store.ListTasks(ctx, "", maxListedTasks, 0)
		if err != nil {
			return &archivepb.ArchiveWorkerHeartbeatRsp{RetInfo: inner(err)}, nil
		}
		for _, t := range tasks {
			rsp.Tasks = append(rsp.Tasks, &archivepb.TaskRef{SpaceId: t.SpaceID, DatasetId: t.DatasetID, TaskId: strconv.FormatInt(t.ID, 10)})
		}
	}
	return rsp, nil
}

func (w *WorkerService) PullArchiveRun(ctx context.Context, req *archivepb.PullArchiveRunReq) (*archivepb.PullArchiveRunRsp, error) {
	s := w.svc
	if req.GetFreeSlots() == 0 {
		return &archivepb.PullArchiveRunRsp{RetInfo: success()}, nil
	}
	now := s.clock()
	// 先取对象存储是否启用：放在领取之后出错，已领取的运行就只能等租约过期。
	status, err := s.workerStatus(ctx)
	if err != nil {
		return &archivepb.PullArchiveRunRsp{RetInfo: inner(err)}, nil
	}
	// 在领取前预解析全部待领运行涉及的空间时区，只领取时区解析成功的空间：
	// 防止 Storage 瞬断或空间被删时已把运行置为 running 锁死 10 分钟租约。
	queuedSpaces, err := s.store.QueuedSpaces(ctx)
	if err != nil {
		return &archivepb.PullArchiveRunRsp{RetInfo: inner(err)}, nil
	}
	if len(queuedSpaces) == 0 {
		return &archivepb.PullArchiveRunRsp{RetInfo: success()}, nil
	}
	spaceTZs := make(map[string]string, len(queuedSpaces))
	allowedSpaces := make(map[string]bool, len(queuedSpaces))
	var firstTZErr error
	for _, spaceID := range queuedSpaces {
		tz, tzErr := s.spaceTimezone(ctx, spaceID)
		if tzErr != nil {
			if firstTZErr == nil {
				firstTZErr = tzErr
			}
			log.WarnContextf(ctx, "archive PullArchiveRun: skip space %s because timezone resolution failed: %v", spaceID, tzErr)
			continue
		}
		spaceTZs[spaceID] = tz
		allowedSpaces[spaceID] = true
	}
	if len(allowedSpaces) == 0 {
		return &archivepb.PullArchiveRunRsp{RetInfo: inner(firstTZErr)}, nil
	}
	claimed, err := s.store.ClaimRuns(ctx, store.ClaimRequest{
		FreeSlots: int(req.GetFreeSlots()), MaxStorageRuns: int(req.GetMaxStorageRuns()),
		BusyStorageRuns: int(req.GetBusyStorageRuns()), Now: now, LeaseTTL: s.cfg.Run.LeaseTTL,
		AllowedSpaces: allowedSpaces,
	})
	if err != nil {
		return &archivepb.PullArchiveRunRsp{RetInfo: inner(err)}, nil
	}
	synced := make(map[string]string, len(req.GetSyncedTasks()))
	for _, ref := range req.GetSyncedTasks() {
		if ref.GetSpaceId() != "" && ref.GetDatasetId() != "" && ref.GetTaskId() != "" {
			synced[ref.GetSpaceId()+"/"+ref.GetDatasetId()] = ref.GetTaskId()
		}
	}
	rsp := &archivepb.PullArchiveRunRsp{RetInfo: success()}
	for _, c := range claimed {
		// 仅在暂存库新建/丢失（未携带匹配的 task_id）、任务重建（task_id 变化）或导入时按需同步；
		// 一旦同步便一次性下发该任务的全部已登记分区（since 为零值），保证暂存库重建后超保留期旧分区
		// 仍可直接在纯本地模式下校验下载，且日后启用外部 S3 也不会漏传历史分区。
		taskID := strconv.FormatInt(c.Task.ID, 10)
		var registered []store.Partition
		if c.Run.Kind == store.KindImport || synced[c.Task.SpaceID+"/"+c.Task.DatasetID] != taskID {
			registered, err = s.store.TaskPartitions(ctx, c.Task.SpaceID, c.Task.DatasetID, time.Time{}, status.remoteEnabled)
			if err != nil {
				// 运行已领取：读不到分区登记就让它照常下发会冒用残缺的本地状态，宁可让租约过期后重来。
				return &archivepb.PullArchiveRunRsp{RetInfo: inner(err)}, nil
			}
		}
		rsp.Runs = append(rsp.Runs, s.assignment(c, registered, spaceTZs[c.Task.SpaceID]))
	}
	return rsp, nil
}

// assignment 把领取到的运行与任务打包成下发给 worker 的参数。
func (s *Service) assignment(c store.Claimed, registered []store.Partition, spaceTZ string) *archivepb.RunAssignment {
	spaceCfg := s.cfg.Space(c.Task.SpaceID)
	reports := make([]*archivepb.PartitionReport, 0, len(registered))
	for _, p := range registered {
		reports = append(reports, registeredPartitionToPB(p))
	}
	return &archivepb.RunAssignment{
		RunId: c.Run.RunID, LeaseToken: c.Run.LeaseToken, Kind: c.Run.Kind, SpaceId: c.Task.SpaceID, DatasetId: c.Task.DatasetID,
		Purpose: c.Task.Purpose, DirName: c.Task.DirName,
		Freq: c.Task.Freq, PartitionUnit: c.Task.PartitionUnit, Retention: c.Task.Retention,
		AllowedLatenessSeconds: uint32(s.cfg.AllowedLateness / time.Second),
		SpaceTz:                spaceTZ, LocalClose: spaceCfg.LocalClose, StagingRetentionSeconds: uint32(s.cfg.StagingRetention / time.Second),
		ScheduledAt: millisOf(c.Run.ScheduledAt), StartedAt: millisOf(c.Run.StartedAt), RangeStart: fmtRFC(c.Run.RangeStart), RangeEnd: fmtRFC(c.Run.RangeEnd),
		BackfillFrom: fmtRFC(c.Task.BackfillFrom), TaskId: strconv.FormatInt(c.Task.ID, 10), DatasetMetaJson: c.Task.MetaJSON, LeaseTtlSeconds: uint32(s.cfg.Run.LeaseTTL / time.Second),
		LastSuccessAt: millisOf(c.Task.LastSuccessAt), RegisteredPartitions: reports,
	}
}

func (w *WorkerService) ReportArchiveRunProgress(ctx context.Context, req *archivepb.ReportArchiveRunProgressReq) (*archivepb.ReportArchiveRunProgressRsp, error) {
	s := w.svc
	if req.GetRunId() == "" || req.GetLeaseToken() == "" {
		return &archivepb.ReportArchiveRunProgressRsp{RetInfo: invalid(errors.New("run_id、lease_token 不能为空"))}, nil
	}
	canceled, err := s.store.Progress(ctx, req.GetRunId(), req.GetLeaseToken(), store.RunStats{
		SubjectsDone: req.GetSubjectsDone(), RequestCount: req.GetRequestCount(),
		BytesRead: req.GetBytesRead(), RowsRead: req.GetRowsRead(),
	}, s.clock(), s.cfg.Run.LeaseTTL)
	if err != nil {
		return &archivepb.ReportArchiveRunProgressRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.ReportArchiveRunProgressRsp{RetInfo: success(), Canceled: canceled}, nil
}

func (w *WorkerService) CompleteArchiveRun(ctx context.Context, req *archivepb.CompleteArchiveRunReq) (*archivepb.CompleteArchiveRunRsp, error) {
	s := w.svc
	if req.GetRunId() == "" || req.GetLeaseToken() == "" {
		return &archivepb.CompleteArchiveRunRsp{RetInfo: invalid(errors.New("run_id、lease_token 不能为空"))}, nil
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
		RunID: req.GetRunId(), LeaseToken: req.GetLeaseToken(), Status: req.GetStatus(), Message: req.GetMessage(),
		Stats: store.RunStats{
			SubjectsDone: req.GetSubjectsDone(), RequestCount: req.GetRequestCount(), BytesRead: req.GetBytesRead(),
			RowsRead: req.GetRowsRead(), RowsAdded: req.GetRowsAdded(), RowsChanged: req.GetRowsChanged(),
			PartitionsExported: req.GetPartitionsExported(), PartitionsFinalized: req.GetPartitionsFinalized(),
			PartitionsUploaded: req.GetPartitionsUploaded(),
		},
		Unrecoverable: unrecoverable, Partitions: partitions, FinalUntil: finalUntil, Now: s.clock(),
	})
	if err != nil {
		return &archivepb.CompleteArchiveRunRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.CompleteArchiveRunRsp{RetInfo: success()}, nil
}

// registeredPartitionToPB 把一条分区登记转成下发给 worker 的报告（不带预览与列名，只用来补齐本地分区状态）。
// 必须带上远端字段：暂存库重建后据此从对象存储取回本地没有的文件。
func registeredPartitionToPB(p store.Partition) *archivepb.PartitionReport {
	return &archivepb.PartitionReport{
		PartitionKey: p.PartitionKey, RangeStart: fmtRFC(p.RangeStart), RangeEnd: fmtRFC(p.RangeEnd), Status: p.Status,
		RowCount: p.RowCount, SubjectCount: p.SubjectCount, MinDataTime: fmtRFC(p.MinDataTime), MaxDataTime: fmtRFC(p.MaxDataTime),
		FilePath: p.FilePath, FileBytes: p.FileBytes, FileSha256: p.FileSHA256, ExportCount: p.ExportCount,
		LastChangedAt: millisOf(p.LastChangedAt), ExportedAt: millisOf(p.ExportedAt), FinalizedAt: millisOf(p.FinalizedAt),
		RemoteKey: p.Remote.Key, RemoteSha256: p.Remote.SHA256, RemoteEtag: p.Remote.ETag, RemoteUploadedAt: millisOf(p.Remote.UploadedAt),
		RemoteVerifiedAt: millisOf(p.Remote.VerifiedAt), RemoteError: p.Remote.Error,
	}
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
			Remote:      store.Remote{Key: item.GetRemoteKey(), SHA256: item.GetRemoteSha256(), ETag: item.GetRemoteEtag(), Error: item.GetRemoteError()},
		}
		// 分区范围与数据时间是 data_time 口径的文本，其余是 Unix 毫秒
		var err error
		for _, field := range []struct {
			name, value string
			dest        *time.Time
		}{
			{"range_start", item.GetRangeStart(), &p.RangeStart}, {"range_end", item.GetRangeEnd(), &p.RangeEnd},
			{"min_data_time", item.GetMinDataTime(), &p.MinDataTime}, {"max_data_time", item.GetMaxDataTime(), &p.MaxDataTime},
		} {
			if *field.dest, err = parseRFC(fmt.Sprintf("分区 %s 的 %s", key, field.name), field.value); err != nil {
				return nil, err
			}
		}
		for _, field := range []struct {
			name  string
			value int64
			dest  *time.Time
		}{
			{"last_changed_at", item.GetLastChangedAt(), &p.LastChangedAt}, {"exported_at", item.GetExportedAt(), &p.ExportedAt},
			{"finalized_at", item.GetFinalizedAt(), &p.FinalizedAt},
			{"remote_uploaded_at", item.GetRemoteUploadedAt(), &p.Remote.UploadedAt}, {"remote_verified_at", item.GetRemoteVerifiedAt(), &p.Remote.VerifiedAt},
		} {
			if field.value < 0 {
				return nil, fmt.Errorf("分区 %s 的 %s 必须是非负的 Unix 毫秒", key, field.name)
			}
			*field.dest = timeOfMillis(field.value)
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
