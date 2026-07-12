package hostmetrics

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"math"
	"testing"
	"time"
)

func TestIdleFetchTimeoutKeepsConsumerRunning(t *testing.T) {
	if !isIdleFetchError(nats.ErrTimeout) {
		t.Fatal("NATS idle fetch timeout must not restart the durable consumer")
	}
	if isIdleFetchError(errors.New("connection closed")) {
		t.Fatal("non-timeout consumer errors must still be returned")
	}
}

func TestValidateHostMetricContract(t *testing.T) {
	payload, err := proto.Marshal(&hostmetricpb.HostMetric{Snapshot: &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1, UsageAvailable: true, UsagePercent: 20}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40}}})
	if err != nil {
		t.Fatal(err)
	}
	now := timestamppb.Now()
	msg := &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", Topic: Topic, Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, Producer: &messagepb.Producer{ServiceName: "moox-host-agent", InstanceId: "0190f4d0-7b1c-4f45-9a3e-7c28f6479a73", NodeId: "host", BootId: "boot"}, SpaceId: SpaceID, OccurredAt: now, PublishedAt: now, ContentType: ContentType, Payload: payload}
	if _, err := ValidateMessage(msg); err != nil {
		t.Fatal(err)
	}
	msg.SpaceId = "crypto"
	if _, err := ValidateMessage(msg); err == nil {
		t.Fatal("non-system space accepted")
	}
}

func TestEvaluateNilInputsShouldNoop(t *testing.T) {
	var evaluator *AlertEvaluator
	assert.NoError(t, evaluator.Evaluate(context.Background(), "agent-1", "msg-1", &hostmetricpb.HostSnapshot{}, time.Now()))

	evaluator = &AlertEvaluator{}
	assert.NoError(t, evaluator.Evaluate(context.Background(), "agent-1", "msg-1", nil, time.Now()))
}

func TestPositiveDefaultsToOne(t *testing.T) {
	assert.Equal(t, 1, positive(0))
	assert.Equal(t, 3, positive(3))
}

func TestDeterministicEventIDStable(t *testing.T) {
	first := deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered)
	second := deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered)
	assert.Equal(t, first, second)
	assert.NotEqual(t, first, deterministicEventID("msg-2", "rule-1", domain.AlertEventTriggered))
}

func TestHostThresholdsUsesNetworkDefault(t *testing.T) {
	threshold, recovery := hostThresholds(domain.AlertRule{CheckID: HostMetricNetworkErrors}, HostMetricNetworkErrors)
	assert.Equal(t, 1.0, threshold)
	assert.Equal(t, 1.0, recovery)
}

type fakeSnapshotWriter struct {
	err       error
	calls     int
	lastID    string
	lastAgent string
	lastSnap  *hostmetricpb.HostSnapshot
}

func (w *fakeSnapshotWriter) WriteSnapshot(_ context.Context, snapshot *hostmetricpb.HostSnapshot, agentID string, _ time.Time, messageID string) error {
	w.calls++
	w.lastID, w.lastAgent, w.lastSnap = messageID, agentID, proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
	return w.err
}

func validHostDelivery(t *testing.T) *jetstream.Delivery {
	t.Helper()
	now := timestamppb.Now()
	metric := &hostmetricpb.HostMetric{Snapshot: &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50}}}
	payload, err := proto.Marshal(metric)
	if err != nil {
		t.Fatal(err)
	}
	return &jetstream.Delivery{Message: &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", Topic: Topic, Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, Producer: &messagepb.Producer{ServiceName: "moox-host-agent", InstanceId: "0190f4d0-7b1c-4f45-9a3e-7c28f6479a73", NodeId: "host", BootId: "boot"}, SpaceId: SpaceID, OccurredAt: now, PublishedAt: now, ContentType: ContentType, Payload: payload}}
}

func TestStorePersistsToStorageBeforeUpdatingLatest(t *testing.T) {
	writer := &fakeSnapshotWriter{}
	store := NewStoreWithWriter(writer)
	delivery := validHostDelivery(t)
	metric, err := ValidateMessage(delivery.Message)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.persist(context.Background(), delivery, metric); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || writer.lastAgent == "" || writer.lastID == "" {
		t.Fatalf("writer calls=%d agent=%q id=%q", writer.calls, writer.lastAgent, writer.lastID)
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	delivery.Message.GetProducer().NodeId = "mutated"
	if agents[0].Hostname != "host" {
		t.Fatalf("latest view was not captured immutably: %+v", agents[0])
	}
}

func TestStoreLeavesLatestUnchangedWhenStorageFails(t *testing.T) {
	writer := &fakeSnapshotWriter{err: errors.New("storage unavailable")}
	store := NewStoreWithWriter(writer)
	delivery := validHostDelivery(t)
	metric, _ := ValidateMessage(delivery.Message)
	if err := store.persist(context.Background(), delivery, metric); err == nil {
		t.Fatal("storage failure unexpectedly succeeded")
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("latest registry updated after failed storage write: %+v", agents)
	}
}

func TestStoreHistoryIsOwnedByStorage(t *testing.T) {
	store := NewStoreWithWriter(&fakeSnapshotWriter{})
	history, err := store.History(context.Background(), "agent", time.Time{}, time.Now(), 100)
	if err != nil || len(history) != 0 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

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
