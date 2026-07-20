package hostmetrics

import (
	"context"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type readerAccessFake struct {
	calls     int
	rows      []*storagepb.TimeSeriesRow
	lastStart string
	lastEnd   string
}

func (f *readerAccessFake) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.calls++
	f.lastStart, f.lastEnd = req.GetTimeRange().GetStartTime(), req.GetTimeRange().GetEndTime()
	dataset := req.GetKeys()[0].GetDatasetId()
	var rows []*storagepb.TimeSeriesRow
	for _, row := range f.rows {
		if row.GetKey().GetDatasetId() == dataset {
			rows = append(rows, row)
		}
	}
	start, end := 0, len(rows)
	if req.GetPage().GetCursor() == "cursor-1" {
		start = 1
	}
	if start >= len(rows) {
		return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
	}
	if end > start+1 {
		end = start + 1
	}
	page := &storagepb.PageResult{HasMore: end < len(rows)}
	if page.HasMore {
		page.NextCursor = "cursor-1"
	}
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Rows: rows[start:end], PageResult: page}, nil
}

func TestStorageReaderPaginatesAndRebuildsEntities(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	fake := &readerAccessFake{rows: []*storagepb.TimeSeriesRow{
		resourceRow(SpaceID, cfg.ResourceDatasetID, "1m", at, &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100}}, "agent-1", "m1"),
		filesystemRow(SpaceID, cfg.FilesystemDatasetID, "1m", at, &hostmetricpb.FilesystemMetric{Device: "sda1", Mountpoint: "/", UsagePercent: 70}, "agent-1", "m1"),
		filesystemRow(SpaceID, cfg.FilesystemDatasetID, "1m", at, &hostmetricpb.FilesystemMetric{Device: "sdb1", Mountpoint: "/data", UsagePercent: 20}, "agent-1", "m1"),
	}}
	reader := NewStorageReader(fake, cfg)
	points, err := reader.History(context.Background(), "agent-1", time.Unix(0, 0), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || len(points[0].Snapshot.GetFilesystems()) != 2 || fake.calls < 5 {
		t.Fatalf("points=%+v calls=%d", points, fake.calls)
	}
	start, _ := time.Parse(time.RFC3339Nano, fake.lastStart)
	end, _ := time.Parse(time.RFC3339Nano, fake.lastEnd)
	if end.Sub(start) > 7*24*time.Hour {
		t.Fatalf("reader exceeded seven-day bound: %s to %s", fake.lastStart, fake.lastEnd)
	}
}
