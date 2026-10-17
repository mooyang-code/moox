package service

import (
	"fmt"
	"time"

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
	}
	for _, item := range r.Unrecoverable {
		out.Unrecoverable = append(out.Unrecoverable, &archivepb.UnrecoverableRange{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag, StartTime: item.StartTime, EndTime: item.EndTime})
	}
	return out
}

func partitionToPB(p store.Partition) *archivepb.ArchivePartition {
	return &archivepb.ArchivePartition{
		SpaceId: p.SpaceID, DatasetId: p.DatasetID, PartitionKey: p.PartitionKey, RangeStart: fmtRFC(p.RangeStart), RangeEnd: fmtRFC(p.RangeEnd),
		Status: p.Status, WorkerId: p.WorkerID, RowCount: p.RowCount, SubjectCount: p.SubjectCount, MinDataTime: fmtRFC(p.MinDataTime), MaxDataTime: fmtRFC(p.MaxDataTime),
		FilePath: p.FilePath, FileBytes: p.FileBytes, FileSha256: p.FileSHA256, Columns: append([]string(nil), p.Columns...), Preview: p.Preview,
		ExportCount: p.ExportCount, LastChangedAt: fmtRFC(p.LastChangedAt), ExportedAt: fmtRFC(p.ExportedAt), FinalizedAt: fmtRFC(p.FinalizedAt), LastRunId: p.LastRunID,
	}
}

func workerToPB(w store.Worker, now time.Time) *archivepb.ArchiveWorkerInfo {
	return &archivepb.ArchiveWorkerInfo{
		WorkerId: w.WorkerID, InstanceId: w.InstanceID, Version: w.Version, RootDir: w.RootDir, DiskTotalBytes: w.DiskTotalBytes,
		DiskFreeBytes: w.DiskFreeBytes, StagingBytes: w.StagingBytes, Maintaining: w.Maintaining, PacingJson: w.PacingJSON,
		RunningRunIds: append([]string(nil), w.RunningRunIDs...), LeaseUntil: fmtRFC(w.LeaseUntil), LastHeartbeatAt: fmtRFC(w.LastHeartbeat), Active: w.Active(now),
	}
}

func taskToPB(t store.Task) *archivepb.ArchiveTask {
	return &archivepb.ArchiveTask{
		SpaceId: t.SpaceID, DatasetId: t.DatasetID, Freq: t.Freq, PartitionUnit: t.PartitionUnit, DataNodeId: t.DataNodeID, Retention: t.Retention,
		ScheduleCron: t.ScheduleCron, ScheduleTz: t.ScheduleTZ, IntervalSeconds: uint32(t.IntervalSeconds), StartAt: fmtRFC(t.StartAt),
		BackfillFrom: fmtRFC(t.BackfillFrom), Status: t.Status, NextRunAt: fmtRFC(t.NextRunAt), ConsecutiveSkips: uint32(t.ConsecutiveSkips),
		LastSuccessAt: fmtRFC(t.LastSuccessAt), FinalUntil: fmtRFC(t.FinalUntil), Creator: t.Creator, CreatedAt: fmtRFC(t.CreatedAt), UpdatedAt: fmtRFC(t.UpdatedAt),
	}
}
