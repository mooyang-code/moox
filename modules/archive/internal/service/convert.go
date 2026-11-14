package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

func fmtRFC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseRFC(field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 必须是 RFC3339 时间: %w", field, err)
	}
	return t.UTC(), nil
}

func runToPB(r store.Run) *archivepb.ArchiveRun {
	out := &archivepb.ArchiveRun{
		RunId: r.RunID, SpaceId: r.SpaceID, DatasetId: r.DatasetID, Kind: r.Kind, RangeStart: fmtRFC(r.RangeStart), RangeEnd: fmtRFC(r.RangeEnd),
		Status: r.Status, ScheduledAt: fmtRFC(r.ScheduledAt), StartedAt: fmtRFC(r.StartedAt), FinishedAt: fmtRFC(r.FinishedAt),
		WorkerId: r.WorkerID, LeaseUntil: fmtRFC(r.LeaseUntil), SubjectsTotal: r.Stats.SubjectsTotal, SubjectsDone: r.Stats.SubjectsDone,
		RequestCount: r.Stats.RequestCount, BytesRead: r.Stats.BytesRead, RowsRead: r.Stats.RowsRead, RowsAdded: r.Stats.RowsAdded,
		RowsChanged: r.Stats.RowsChanged, PartitionsExported: r.Stats.PartitionsExported, PartitionsFinalized: r.Stats.PartitionsFinalized, Message: r.Message,
		PartitionsUploaded: r.Stats.PartitionsUploaded, ImportFile: r.ImportFile, Overwrite: r.Overwrite,
	}
	for _, item := range r.Unrecoverable {
		out.Unrecoverable = append(out.Unrecoverable, &archivepb.UnrecoverableRange{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag, StartTime: item.StartTime, EndTime: item.EndTime})
	}
	return out
}

// 远端副本状态（remote_state）。
const (
	RemoteOff      = "off"
	RemoteNone     = "none"
	RemoteVerified = "verified"
	RemoteFailed   = "failed"
	RemotePending  = "pending"
)

// remoteState 按 4.3 节的口径计算分区的远端副本状态；remoteEnabled 为假时一律 off。
func remoteState(p store.Partition, remoteEnabled bool) string {
	switch {
	case !remoteEnabled:
		return RemoteOff
	case p.FilePath == "":
		return RemoteNone
	case p.Replicated():
		return RemoteVerified
	case p.Remote.Error != "":
		return RemoteFailed
	default:
		return RemotePending
	}
}

func partitionToPB(p store.Partition, remoteEnabled bool) *archivepb.ArchivePartition {
	return &archivepb.ArchivePartition{
		SpaceId: p.SpaceID, DatasetId: p.DatasetID, PartitionKey: p.PartitionKey, RangeStart: fmtRFC(p.RangeStart), RangeEnd: fmtRFC(p.RangeEnd),
		Status: p.Status, WorkerId: p.WorkerID, RowCount: p.RowCount, SubjectCount: p.SubjectCount, MinDataTime: fmtRFC(p.MinDataTime), MaxDataTime: fmtRFC(p.MaxDataTime),
		FilePath: p.FilePath, FileBytes: p.FileBytes, FileSha256: p.FileSHA256, Columns: append([]string(nil), p.Columns...), Preview: p.Preview,
		ExportCount: p.ExportCount, LastChangedAt: fmtRFC(p.LastChangedAt), ExportedAt: fmtRFC(p.ExportedAt), FinalizedAt: fmtRFC(p.FinalizedAt), LastRunId: p.LastRunID,
		RemoteKey: p.Remote.Key, RemoteSha256: p.Remote.SHA256, RemoteEtag: p.Remote.ETag, RemoteUploadedAt: fmtRFC(p.Remote.UploadedAt),
		RemoteVerifiedAt: fmtRFC(p.Remote.VerifiedAt), RemoteError: p.Remote.Error, RemoteState: remoteState(p, remoteEnabled),
	}
}

// objectStoreJSON 是 t_archive_workers.c_object_store_json 的格式（字段名同 ObjectStoreInfo）。
type objectStoreJSON struct {
	Endpoint     string `json:"endpoint,omitempty"`
	Bucket       string `json:"bucket,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
	StorageClass string `json:"storage_class,omitempty"`
	URLStyle     string `json:"url_style,omitempty"`
	// ProbeError、UnavailableSince 是 worker 最近一次探测的结果，可用时为空。
	ProbeError       string `json:"probe_error,omitempty"`
	UnavailableSince string `json:"unavailable_since,omitempty"`
}

func objectStoreToJSON(info *archivepb.ObjectStoreInfo) string {
	raw, _ := json.Marshal(objectStoreJSON{
		Endpoint: info.GetEndpoint(), Bucket: info.GetBucket(), Prefix: info.GetPrefix(), StorageClass: info.GetStorageClass(), URLStyle: info.GetUrlStyle(),
		ProbeError: info.GetProbeError(), UnavailableSince: info.GetUnavailableSince(),
	})
	return string(raw)
}

func objectStoreFromJSON(text string) *archivepb.ObjectStoreInfo {
	var v objectStoreJSON
	if text != "" {
		_ = json.Unmarshal([]byte(text), &v)
	}
	return &archivepb.ObjectStoreInfo{
		Endpoint: v.Endpoint, Bucket: v.Bucket, Prefix: v.Prefix, StorageClass: v.StorageClass, UrlStyle: v.URLStyle,
		ProbeError: v.ProbeError, UnavailableSince: v.UnavailableSince,
	}
}

func workerToPB(w store.Worker, now time.Time) *archivepb.ArchiveWorkerInfo {
	return &archivepb.ArchiveWorkerInfo{
		WorkerId: w.WorkerID, InstanceId: w.InstanceID, Version: w.Version, RootDir: w.RootDir, DiskTotalBytes: w.DiskTotalBytes,
		DiskFreeBytes: w.DiskFreeBytes, DiskUsedBytes: w.DiskUsedBytes, MinFreeBytes: w.MinFreeBytes, StagingBytes: w.StagingBytes, Maintaining: w.Maintaining, PacingJson: w.PacingJSON,
		RunningRunIds: append([]string(nil), w.RunningRunIDs...), LeaseUntil: fmtRFC(w.LeaseUntil), LastHeartbeatAt: fmtRFC(w.LastHeartbeat), Active: w.Active(now),
		ObjectStore: objectStoreFromJSON(w.ObjectStoreJSON),
	}
}

// taskToPB 把任务转成接口消息；启用的任务带上下一次拉取时刻（算不出时留空）。
func (s *Service) taskToPB(t store.Task, now time.Time) *archivepb.ArchiveTask {
	out := &archivepb.ArchiveTask{
		SpaceId: t.SpaceID, DatasetId: t.DatasetID, Purpose: t.Purpose, DirName: t.DirName, Freq: t.Freq, PartitionUnit: t.PartitionUnit, Retention: t.Retention,
		PullEveryBars: uint32(t.PullEveryBars), IntervalSeconds: uint32(t.IntervalSeconds), BackfillFrom: fmtRFC(t.BackfillFrom), Status: t.Status,
		LastBoundary: fmtRFC(t.LastBoundary), LastReview: fmtRFC(t.LastReview), ConsecutiveSkips: uint32(t.ConsecutiveSkips),
		LastSuccessAt: fmtRFC(t.LastSuccessAt), FinalUntil: fmtRFC(t.FinalUntil), Creator: t.Creator, CreatedAt: fmtRFC(t.CreatedAt), UpdatedAt: fmtRFC(t.UpdatedAt),
		BlockedReason: t.BlockedReason,
	}
	if t.Status == store.StatusEnabled {
		if pulls, err := schedule.NextPulls(s.timing(t.SpaceID, t.Freq, t.PullEveryBars), now, 1); err == nil && len(pulls) > 0 {
			out.NextPullAt = fmtRFC(pulls[0])
		}
	}
	for _, item := range t.Unrecoverable {
		out.Unrecoverable = append(out.Unrecoverable, &archivepb.UnrecoverableRange{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag, StartTime: item.StartTime, EndTime: item.EndTime})
	}
	return out
}
