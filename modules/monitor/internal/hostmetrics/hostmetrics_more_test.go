package hostmetrics

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreHelpersAndHistoryFallback(t *testing.T) {
	store := NewStore(struct{}{})
	require.NotNil(t, store)
	assert.NoError(t, store.EnsureSchema())
	assert.True(t, store.StorageReady())

	store.SetStorageReady(func() bool { return false })
	assert.False(t, store.StorageReady())
	store.SetAlertEvaluator(&AlertEvaluator{})

	points, err := store.History(context.Background(), "agent-1", time.Now().Add(-time.Hour), time.Now(), 10)
	require.NoError(t, err)
	assert.Empty(t, points)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.History(ctx, "agent-1", time.Now().Add(-time.Hour), time.Now(), 10)
	require.Error(t, err)

	assert.Nil(t, NewDLQPublisher(nil))
	assert.NoError(t, (*Consumer)(nil).Close())
}

func TestValidateSnapshotRejectsInvalidBranches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot *hostmetricpb.HostSnapshot
	}{
		{"missing_cpu", &hostmetricpb.HostSnapshot{Memory: validMemoryMetric()}},
		{"zero_logical_cores", &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{}, Memory: validMemoryMetric()}},
		{"bad_cpu_percent", &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1, UsageAvailable: true, UsagePercent: 101}, Memory: validMemoryMetric()}},
		{"bad_memory_bounds", &hostmetricpb.HostSnapshot{Cpu: validCPUMetric(), Memory: &hostmetricpb.MemoryMetric{TotalBytes: 10, UsedBytes: 11, UsagePercent: 50}}},
		{"empty_filesystem_mount", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Filesystems = []*hostmetricpb.FilesystemMetric{{Device: "sda1"}}
		})},
		{"duplicate_filesystem", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Filesystems = []*hostmetricpb.FilesystemMetric{{Device: "sda1", Mountpoint: "/"}, {Device: "sda1", Mountpoint: "/"}}
		})},
		{"filesystem_counter_overflow", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Filesystems = []*hostmetricpb.FilesystemMetric{{Device: "sda1", Mountpoint: "/", TotalBytes: 1 << 63}}
		})},
		{"filesystem_percent_invalid", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Filesystems = []*hostmetricpb.FilesystemMetric{{Device: "sda1", Mountpoint: "/", UsagePercent: -1}}
		})},
		{"empty_disk_device", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Disks = []*hostmetricpb.DiskMetric{{}}
		})},
		{"duplicate_disk", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Disks = []*hostmetricpb.DiskMetric{{Device: "sda"}, {Device: "sda"}}
		})},
		{"disk_counter_overflow", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Disks = []*hostmetricpb.DiskMetric{{Device: "sda", ReadBytesTotal: 1 << 63}}
		})},
		{"disk_rate_invalid", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Disks = []*hostmetricpb.DiskMetric{{Device: "sda", RateAvailable: true, ReadBytesPerSecond: math.NaN()}}
		})},
		{"disk_utilization_invalid", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Disks = []*hostmetricpb.DiskMetric{{Device: "sda", RateAvailable: true, UtilizationPercent: 101}}
		})},
		{"empty_network_device", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Networks = []*hostmetricpb.NetworkMetric{{}}
		})},
		{"duplicate_network", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Networks = []*hostmetricpb.NetworkMetric{{Device: "eth0"}, {Device: "eth0"}}
		})},
		{"network_counter_overflow", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Networks = []*hostmetricpb.NetworkMetric{{Device: "eth0", ReceiveBytesTotal: 1 << 63}}
		})},
		{"network_rate_invalid", validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
			s.Networks = []*hostmetricpb.NetworkMetric{{Device: "eth0", RateAvailable: true, ReceiveBytesPerSecond: math.Inf(1)}}
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, validateSnapshot(tc.snapshot))
		})
	}
}

func TestHostValuesRetryDelayAndRejectionMessage(t *testing.T) {
	snapshot := validSnapshotWith(func(s *hostmetricpb.HostSnapshot) {
		s.Cpu.UsageAvailable = false
		s.Memory.UsagePercent = 42
		s.Filesystems = []*hostmetricpb.FilesystemMetric{
			{Device: "sda1", Mountpoint: "/", UsagePercent: 60},
			{Device: "sdb1", Mountpoint: "/data", UsagePercent: 75},
		}
		s.Disks = []*hostmetricpb.DiskMetric{
			{Device: "sda", RateAvailable: true, UtilizationPercent: 55},
			{Device: "sdb", RateAvailable: true, UtilizationPercent: 80},
		}
		s.Networks = []*hostmetricpb.NetworkMetric{
			{Device: "eth0", RateAvailable: true, ReceiveErrorsTotal: 2, TransmitErrorsTotal: 3},
			{Device: "eth1", RateAvailable: true, ReceiveErrorsTotal: 5},
		}
	})
	values := hostValues(snapshot)
	assert.False(t, values[HostMetricCPU].available)
	assert.Equal(t, 42.0, values[HostMetricMemory].value)
	assert.Equal(t, 75.0, values[HostMetricFilesystemUsage].value)
	assert.Equal(t, 80.0, values[HostMetricDiskUtilization].value)
	assert.Equal(t, 10.0, values[HostMetricNetworkErrors].value)

	assert.Equal(t, time.Second, retryDelay(1))
	assert.Equal(t, 5*time.Second, retryDelay(2))
	assert.Equal(t, 15*time.Second, retryDelay(3))

	msg := rejectionMessage(&jetstream.Delivery{
		Subject:      "host.metrics",
		RawData:      []byte("bad"),
		RawMessageID: "raw-id",
		Message:      &messagepb.MooxMessage{MessageId: "msg-id"},
	}, "decode failed")
	assert.Equal(t, "msg-id.rejected", msg.GetMessageId())
	assert.Equal(t, "host.metrics", msg.GetAttributes()["original_topic"])
	assert.Equal(t, "decode failed", msg.GetAttributes()["rejection_reason"])

	generated := rejectionMessage(nil, "missing")
	assert.Contains(t, generated.GetMessageId(), "invalid-host-metric-")
}

func validCPUMetric() *hostmetricpb.CpuMetric {
	return &hostmetricpb.CpuMetric{LogicalCores: 2, UsageAvailable: true, UsagePercent: 50}
}

func validMemoryMetric() *hostmetricpb.MemoryMetric {
	return &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40}
}

func validSnapshotWith(mutator func(*hostmetricpb.HostSnapshot)) *hostmetricpb.HostSnapshot {
	snapshot := &hostmetricpb.HostSnapshot{Cpu: validCPUMetric(), Memory: validMemoryMetric()}
	if mutator != nil {
		mutator(snapshot)
	}
	return snapshot
}
