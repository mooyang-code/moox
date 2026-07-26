package hostmetrics

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHostMetric() *hostmetricpb.HostMetric {
	return &hostmetricpb.HostMetric{AgentId: "0190f4d0-7b1c-4f45-9a3e-7c28f6479a73", Hostname: "host", BootId: "boot", AgentVersion: "test", Snapshot: &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
	}}
}

func validHostMessage(t *testing.T) *eventpb.EventMessage {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.MetricsHostReported, validHostMetric(), events.PublishOptions{EventID: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", OccurredAt: time.Now().UTC(), SpaceID: SpaceID, SubjectID: validHostMetric().GetAgentId()})
	require.NoError(t, err)
	return encoded.Message
}

func TestValidateHostMetricContract(t *testing.T) {
	message := validHostMessage(t)
	metric, err := ValidateMessage(message)
	require.NoError(t, err)
	assert.Equal(t, message.GetSubjectId(), metric.GetAgentId())
	message.SpaceId = "crypto"
	assert.Error(t, func() error { _, err := ValidateMessage(message); return err }())
}

type fakeSnapshotWriter struct {
	err       error
	calls     int
	lastID    string
	lastAgent string
}

func (w *fakeSnapshotWriter) WriteSnapshot(_ context.Context, snapshot *hostmetricpb.HostSnapshot, agentID string, _ time.Time, messageID string) error {
	w.calls++
	w.lastID, w.lastAgent = messageID, agentID
	if snapshot == nil {
		return errors.New("snapshot is nil")
	}
	return w.err
}

func TestStorePersistsBeforeUpdatingLatest(t *testing.T) {
	writer := &fakeSnapshotWriter{}
	store := NewStoreWithWriter(writer)
	message := validHostMessage(t)
	metric, err := ValidateMessage(message)
	require.NoError(t, err)
	require.NoError(t, store.Persist(context.Background(), message, metric))
	assert.Equal(t, 1, writer.calls)
	agents, err := store.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "host", agents[0].Hostname)
}

func TestStoreLeavesLatestUnchangedWhenStorageFails(t *testing.T) {
	store := NewStoreWithWriter(&fakeSnapshotWriter{err: errors.New("storage unavailable")})
	message := validHostMessage(t)
	metric, _ := ValidateMessage(message)
	assert.Error(t, store.Persist(context.Background(), message, metric))
	agents, err := store.ListAgents(context.Background())
	require.NoError(t, err)
	assert.Empty(t, agents)
}

func TestStoreHistoryIsOwnedByStorage(t *testing.T) {
	store := NewStoreWithWriter(&fakeSnapshotWriter{})
	history, err := store.History(context.Background(), "agent", time.Time{}, time.Now(), 100)
	require.NoError(t, err)
	assert.Empty(t, history)
}

func TestValidateSnapshotRejectsInvalidBranches(t *testing.T) {
	for _, snapshot := range []*hostmetricpb.HostSnapshot{
		{Memory: validMemoryMetric()},
		{Cpu: &hostmetricpb.CpuMetric{}, Memory: validMemoryMetric()},
		{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1, UsageAvailable: true, UsagePercent: 101}, Memory: validMemoryMetric()},
		{Cpu: validCPUMetric(), Memory: &hostmetricpb.MemoryMetric{TotalBytes: 10, UsedBytes: 11}},
		{Cpu: validCPUMetric(), Memory: validMemoryMetric(), Disks: []*hostmetricpb.DiskMetric{{Device: "sda", RateAvailable: true, ReadBytesPerSecond: math.NaN()}}},
	} {
		require.Error(t, validateSnapshot(snapshot))
	}
}

func TestHostHelpers(t *testing.T) {
	assert.Equal(t, 1, positive(0))
	assert.Equal(t, 3, positive(3))
	first := deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered)
	assert.Equal(t, first, deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered))
	assert.NotEqual(t, first, deterministicEventID("msg-2", "rule-1", domain.AlertEventTriggered))
	threshold, recovery := hostThresholds(domain.AlertRule{CheckID: HostMetricNetworkErrors}, HostMetricNetworkErrors)
	assert.Equal(t, 1.0, threshold)
	assert.Equal(t, 1.0, recovery)
}

func validCPUMetric() *hostmetricpb.CpuMetric {
	return &hostmetricpb.CpuMetric{LogicalCores: 2, UsageAvailable: true, UsagePercent: 50}
}

func validMemoryMetric() *hostmetricpb.MemoryMetric {
	return &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40}
}
