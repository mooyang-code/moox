package test

import (
	"context"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeStorage struct{ rows []*storagepb.RowFieldUpsert }

func (f *fakeStorage) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	for _, incoming := range req.GetRows() {
		replaced := false
		for i, existing := range f.rows {
			if proto.Equal(existing.GetKey(), incoming.GetKey()) {
				f.rows[i] = incoming
				replaced = true
				break
			}
		}
		if !replaced {
			f.rows = append(f.rows, incoming)
		}
	}
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeStorage) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	dataset := req.GetKeys()[0].GetDatasetId()
	rows := make([]*storagepb.TimeSeriesRow, 0)
	for _, row := range f.rows {
		if row.GetKey().GetDatasetId() == dataset {
			key := row.GetKey()
			attributes := make(map[string]string, len(row.GetAttributes()))
			for name, value := range row.GetAttributes() {
				attributes[name] = value.GetStringValue()
			}
			rows = append(rows, &storagepb.TimeSeriesRow{
				Key: &storagepb.TimeSeriesKey{
					SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
					SubjectId: key.GetTimeSeries().GetSubjectId(), Freq: key.GetTimeSeries().GetFreq(), DataTime: key.GetTimeSeries().GetDataTime(),
				},
				Fields: row.GetFields(), Attributes: attributes,
			})
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
	if err := writer.WriteSnapshot(context.Background(), snapshot, "agent-1", observed.Add(2*time.Minute), "message-2"); err != nil {
		t.Fatal(err)
	}
	if len(fake.rows) != 8 {
		t.Fatalf("different minute should add four rows, got %d", len(fake.rows))
	}
	if err := writer.WriteSnapshot(context.Background(), snapshot, "agent-1", observed, "message-duplicate"); err != nil {
		t.Fatal(err)
	}
	if len(fake.rows) != 8 {
		t.Fatalf("same minute duplicate changed row count: %d", len(fake.rows))
	}
	reader := hostmetrics.NewStorageReader(fake, cfg)
	points, err := reader.History(context.Background(), "agent-1", observed.Add(-time.Minute), observed.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Snapshot.GetCpu().GetLogicalCores() != 4 || len(points[0].Snapshot.GetFilesystems()) != 1 || len(points[0].Snapshot.GetDisks()) != 1 || len(points[0].Snapshot.GetNetworks()) != 1 {
		t.Fatalf("round-trip points=%+v", points)
	}
}
