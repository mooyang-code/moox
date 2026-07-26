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
	requests []*storagepb.PrimaryUpsertFieldsReq
}

func (f *writerAccessFake) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	f.requests = append(f.requests, req)
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
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
	if err := writer.WriteSnapshot(context.Background(), snapshot, "agent-1", at); err != nil {
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
			key := row.GetKey().GetTimeSeries()
			if row.GetKey().GetSpaceId() != SpaceID || key.GetSubjectId() != "agent-1" || key.GetFreq() != "1m" || key.GetDataTime() != "2026-07-11T04:34:00Z" {
				t.Fatalf("unexpected key: %+v", row.GetKey())
			}
		}
	}
	for _, req := range fake.requests {
		for _, row := range req.GetRows() {
			if row.GetKey().GetDatasetId() == cfg.DiskDatasetID && row.GetKey().GetTimeSeries().GetDimensions()["device"] != "sdb" {
				t.Fatalf("disk dimensions=%v", row.GetKey().GetTimeSeries().GetDimensions())
			}
			if row.GetKey().GetDatasetId() == cfg.NetworkDatasetID && row.GetKey().GetTimeSeries().GetDimensions()["device"] != "eth0" {
				t.Fatalf("network dimensions=%v", row.GetKey().GetTimeSeries().GetDimensions())
			}
		}
	}
	for _, req := range fake.requests {
		for _, row := range req.GetRows() {
			for _, field := range row.GetFields() {
				if field.GetFieldId() == "read_bytes_per_second" || field.GetFieldId() == "receive_bytes_per_second" {
					t.Fatal("unavailable rate was written")
				}
			}
		}
	}
}
