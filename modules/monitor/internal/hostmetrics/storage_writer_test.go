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

type writerAccessFake struct {
	requests []*storagepb.MergeTimeSeriesRowsReq
}

func (f *writerAccessFake) MergeTimeSeriesRows(_ context.Context, req *storagepb.MergeTimeSeriesRowsReq, _ ...client.Option) (*storagepb.MergeTimeSeriesRowsRsp, error) {
	f.requests = append(f.requests, req)
	return &storagepb.MergeTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func TestHostStorageWriterBucketsAndOmitsUnavailableRates(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	fake := &writerAccessFake{}
	writer := NewStorageWriter(fake, cfg)
	at := time.Date(2026, 7, 11, 12, 34, 56, 123000000, time.FixedZone("CST", 8*3600))
	snapshot := &hostmetricpb.HostSnapshot{
		Cpu:      &hostmetricpb.CpuMetric{LogicalCores: 8, UsagePercent: 42, UsageAvailable: true},
		Memory:   &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40},
		Disks:    []*hostmetricpb.DiskMetric{{Device: "sdb", ReadBytesTotal: 1, RateAvailable: false}},
		Networks: []*hostmetricpb.NetworkMetric{{Device: "eth0", ReceiveBytesTotal: 2, RateAvailable: false}},
	}
	if err := writer.WriteSnapshot(context.Background(), snapshot, "agent-1", at, "msg-1"); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 {
		t.Fatalf("requests=%d, want populated datasets", len(fake.requests))
	}
	for _, req := range fake.requests {
		if len(req.GetRows()) == 0 {
			t.Fatal("empty dataset request")
		}
		for _, row := range req.GetRows() {
			if row.GetKey().GetSpaceId() != SpaceID || row.GetKey().GetSubjectId() != "agent-1" || row.GetKey().GetFreq() != "1m" || row.GetKey().GetDataTime() != "2026-07-11T04:34:00Z" {
				t.Fatalf("unexpected key: %+v", row.GetKey())
			}
			if row.GetAttributes()["message_id"] != "msg-1" || row.GetAttributes()["agent_id"] != "agent-1" {
				t.Fatalf("attributes=%v", row.GetAttributes())
			}
		}
	}
	for _, req := range fake.requests {
		for _, row := range req.GetRows() {
			for _, column := range row.GetColumns() {
				if column.GetColumnName() == "read_bytes_per_second" || column.GetColumnName() == "receive_bytes_per_second" {
					t.Fatal("unavailable rate was written")
				}
			}
		}
	}
}
