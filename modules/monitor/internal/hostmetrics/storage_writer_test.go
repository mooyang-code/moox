package hostmetrics

import (
	"context"
	"fmt"
	"strings"
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
	for _, row := range req.GetRows() {
		tag := row.GetKey().GetTimeSeries().GetSeriesTag()
		if len(tag) > maxSeriesTagBytes || !isASCII(tag) {
			return nil, fmt.Errorf("invalid series tag %q", tag)
		}
	}
	f.requests = append(f.requests, req)
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
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
	if err := writer.WriteSnapshot(context.Background(), snapshot, "agent-1", at, "event-1"); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 {
		t.Fatalf("requests=%d, want populated datasets", len(fake.requests))
	}
	for _, req := range fake.requests {
		if req.GetSourceEventId() != "event-1" {
			t.Fatalf("source_event_id=%q, want event-1", req.GetSourceEventId())
		}
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
			if row.GetKey().GetDatasetId() == cfg.ResourceDatasetID && row.GetKey().GetTimeSeries().GetSeriesTag() != "" {
				t.Fatalf("resource series_tag=%q", row.GetKey().GetTimeSeries().GetSeriesTag())
			}
			if row.GetKey().GetDatasetId() == cfg.DiskDatasetID && row.GetKey().GetTimeSeries().GetSeriesTag() != "device:sdb" {
				t.Fatalf("disk series_tag=%q", row.GetKey().GetTimeSeries().GetSeriesTag())
			}
			if row.GetKey().GetDatasetId() == cfg.NetworkDatasetID && row.GetKey().GetTimeSeries().GetSeriesTag() != "device:eth0" {
				t.Fatalf("network series_tag=%q", row.GetKey().GetTimeSeries().GetSeriesTag())
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

func TestMonitorSeriesTagsRoundTripOpaqueIdentities(t *testing.T) {
	for _, device := range []string{"sda", "disk:1|part/2", "磁盘 甲+乙"} {
		tag := deviceSeriesTag(device)
		got, err := decodeDeviceSeriesTag(tag)
		if err != nil || got != device {
			t.Fatalf("device tag %q round trip: got=%q err=%v", tag, got, err)
		}
	}
	for _, tt := range []struct{ device, mountpoint string }{
		{"sda1", "/"},
		{"dev|ice:/x", "/data|archive:2026"},
		{"磁盘 甲", "/数据/热 存储"},
	} {
		tag := filesystemSeriesTag(tt.device, tt.mountpoint)
		device, mountpoint, err := decodeFilesystemSeriesTag(tag)
		if err != nil || device != tt.device || mountpoint != tt.mountpoint {
			t.Fatalf("filesystem tag %q round trip: got=(%q,%q) err=%v", tag, device, mountpoint, err)
		}
	}
}

func TestMonitorSeriesTagsUseStableBoundedHashFallback(t *testing.T) {
	shortDevice := strings.Repeat("d", maxSeriesTagBytes-len("device:"))
	shortTag := deviceSeriesTag(shortDevice)
	if len(shortTag) != maxSeriesTagBytes {
		t.Fatalf("boundary device tag bytes=%d, want %d", len(shortTag), maxSeriesTagBytes)
	}
	if got, err := decodeDeviceSeriesTag(shortTag); err != nil || got != shortDevice {
		t.Fatalf("boundary device tag did not round trip: got=%q err=%v", got, err)
	}

	longDevice := shortDevice + "x"
	deviceHash := deviceSeriesTag(longDevice)
	if len(deviceHash) > maxSeriesTagBytes || !isASCII(deviceHash) || !strings.HasPrefix(deviceHash, "device-sha256:") {
		t.Fatalf("invalid long device fallback %q", deviceHash)
	}
	if deviceHash != deviceSeriesTag(longDevice) || deviceHash == deviceSeriesTag(longDevice+"y") {
		t.Fatal("device hash fallback is unstable or collided for distinct identities")
	}
	if _, err := decodeDeviceSeriesTag(deviceHash); err == nil || !strings.Contains(err.Error(), "irreversible") {
		t.Fatalf("decode hashed device error=%v, want irreversible error", err)
	}

	shortMountpoint := strings.Repeat("m", maxSeriesTagBytes-len("filesystem:")-len("d|"))
	shortFilesystemTag := filesystemSeriesTag("d", shortMountpoint)
	if len(shortFilesystemTag) != maxSeriesTagBytes {
		t.Fatalf("boundary filesystem tag bytes=%d, want %d", len(shortFilesystemTag), maxSeriesTagBytes)
	}
	if device, mountpoint, err := decodeFilesystemSeriesTag(shortFilesystemTag); err != nil || device != "d" || mountpoint != shortMountpoint {
		t.Fatalf("boundary filesystem tag did not round trip: got=(%q,%q) err=%v", device, mountpoint, err)
	}

	unicodeIdentity := strings.Repeat("磁盘/数据", 40)
	filesystemHash := filesystemSeriesTag(unicodeIdentity, "/"+unicodeIdentity)
	if len(filesystemHash) > maxSeriesTagBytes || !isASCII(filesystemHash) || !strings.HasPrefix(filesystemHash, "filesystem-sha256:") {
		t.Fatalf("invalid Unicode filesystem fallback %q", filesystemHash)
	}
	if filesystemHash != filesystemSeriesTag(unicodeIdentity, "/"+unicodeIdentity) ||
		filesystemHash == filesystemSeriesTag(unicodeIdentity, "/"+unicodeIdentity+"二") {
		t.Fatal("filesystem hash fallback is unstable or collided for distinct identities")
	}
	if _, _, err := decodeFilesystemSeriesTag(filesystemHash); err == nil || !strings.Contains(err.Error(), "irreversible") {
		t.Fatalf("decode hashed filesystem error=%v, want irreversible error", err)
	}
}

func TestHostStorageWriterCompletesLongIdentitySnapshot(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	fake := &writerAccessFake{}
	longA := strings.Repeat("磁盘/数据:", 40)
	longB := strings.Repeat("网卡|接口+", 40)
	snapshot := &hostmetricpb.HostSnapshot{
		Cpu: &hostmetricpb.CpuMetric{}, Memory: &hostmetricpb.MemoryMetric{},
		Filesystems: []*hostmetricpb.FilesystemMetric{
			{Device: longA, Mountpoint: "/" + longA},
			{Device: longA, Mountpoint: "/" + longA + "/二"},
		},
		Disks:    []*hostmetricpb.DiskMetric{{Device: longA}, {Device: longA + "二"}},
		Networks: []*hostmetricpb.NetworkMetric{{Device: longB}, {Device: longB + "二"}},
	}
	if err := NewStorageWriter(fake, cfg).WriteSnapshot(context.Background(), snapshot, "agent-long", time.Now(), "event-long"); err != nil {
		t.Fatalf("WriteSnapshot() with long identities failed: %v", err)
	}
	rows := 0
	for _, req := range fake.requests {
		rows += len(req.GetRows())
	}
	if len(fake.requests) != 4 || rows != 7 {
		t.Fatalf("completed requests=%d rows=%d, want all four datasets and seven rows", len(fake.requests), rows)
	}
}

func TestHostStorageWriterKeepsSameMinuteEntitiesDistinct(t *testing.T) {
	cfg := monconfig.Default().Metrics.HostStorage
	fake := &writerAccessFake{}
	snapshot := &hostmetricpb.HostSnapshot{
		Cpu: &hostmetricpb.CpuMetric{}, Memory: &hostmetricpb.MemoryMetric{},
		Filesystems: []*hostmetricpb.FilesystemMetric{
			{Device: "disk|一", Mountpoint: "/"},
			{Device: "disk|一", Mountpoint: "/data:热"},
		},
		Disks:    []*hostmetricpb.DiskMetric{{Device: "disk|一"}, {Device: "disk:二"}},
		Networks: []*hostmetricpb.NetworkMetric{{Device: "eth/0"}, {Device: "网卡:一"}},
	}
	err := NewStorageWriter(fake, cfg).WriteSnapshot(context.Background(), snapshot, "agent-1", time.Now(), "event-1")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{})
	for _, req := range fake.requests {
		for _, row := range req.GetRows() {
			key := row.GetKey()
			identity := key.GetDatasetId() + "\x00" + key.GetTimeSeries().GetSeriesTag()
			if _, exists := seen[identity]; exists {
				t.Fatalf("same-minute row identity collided: %q", identity)
			}
			seen[identity] = struct{}{}
		}
	}
	if len(seen) != 7 {
		t.Fatalf("distinct row identities=%d, want 7", len(seen))
	}
}
