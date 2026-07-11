package test

import (
	"context"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeStorage struct{ rows []*storagepb.TimeSeriesRow }

func (f *fakeStorage) DeleteTimeSeriesRows(_ context.Context, req *storagepb.DeleteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.DeleteTimeSeriesRowsRsp, error) {
	cutoff := req.GetTimeRange().GetEndTime()
	kept := f.rows[:0]
	var deleted uint32
	for _, row := range f.rows {
		if row.GetKey().GetDatasetId() == req.GetDatasetId() && row.GetKey().GetDataTime() <= cutoff {
			deleted++
			continue
		}
		kept = append(kept, row)
	}
	f.rows = kept
	return &storagepb.DeleteTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Deleted: deleted}, nil
}

func (f *fakeStorage) WriteTimeSeriesRows(_ context.Context, req *storagepb.WriteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error) {
	f.rows = append(f.rows, req.GetRows()...)
	return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeStorage) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	dataset := req.GetKeys()[0].GetDatasetId()
	rows := make([]*storagepb.TimeSeriesRow, 0)
	for _, row := range f.rows {
		if row.GetKey().GetDatasetId() == dataset {
			rows = append(rows, row)
		}
	}
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Rows: rows}, nil
}

func TestHostMetricDirectStorageRoundTrip(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	fake := &fakeStorage{}
	writer := hostmetrics.NewStorageWriter(fake, cfg)
	observed := time.Now().UTC().Truncate(time.Minute)
	snapshot := &hostmetricpb.HostSnapshot{
		Cpu:         &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25},
		Memory:      &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
		Filesystems: []*hostmetricpb.FilesystemMetric{{Device: "/dev/sda1", Mountpoint: "/", TotalBytes: 1000, UsedBytes: 500, AvailableBytes: 500, UsagePercent: 50}},
		Disks:       []*hostmetricpb.DiskMetric{{Device: "sda", ReadBytesTotal: 10, WriteBytesTotal: 20, RateAvailable: true, UtilizationPercent: 10}},
		Networks:    []*hostmetricpb.NetworkMetric{{Device: "eth0", Operstate: "up", ReceiveBytesTotal: 30, TransmitBytesTotal: 40, RateAvailable: true}},
	}
	if err := writer.WriteSnapshot(context.Background(), snapshot, "agent-1", observed, "message-1"); err != nil {
		t.Fatal(err)
	}
	if len(fake.rows) != 4 {
		t.Fatalf("stored rows=%d, want four datasets", len(fake.rows))
	}
	reader := hostmetrics.NewStorageReader(fake, cfg)
	points, err := reader.History(context.Background(), "agent-1", observed.Add(-time.Minute), observed.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Snapshot.GetCpu().GetLogicalCores() != 4 || len(points[0].Snapshot.GetFilesystems()) != 1 || len(points[0].Snapshot.GetDisks()) != 1 || len(points[0].Snapshot.GetNetworks()) != 1 {
		t.Fatalf("round-trip points=%+v", points)
	}
	if deleted, err := hostmetrics.Prune(context.Background(), fake, cfg, observed.Add(73*time.Hour)); err != nil || deleted != 4 {
		t.Fatalf("retention deleted=%d err=%v", deleted, err)
	}
}
