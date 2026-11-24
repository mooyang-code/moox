package hostmetrics

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHostMetric() *hostmetricpb.HostMetric {
	return &hostmetricpb.HostMetric{HostId: "control", Hostname: "host", BootId: "boot", AgentVersion: "test", Snapshot: &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
	}}
}

func validHostMessage(t *testing.T) *eventpb.EventMessage {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.ObservabilityHostSnapshotReported, validHostMetric(), events.PublishOptions{EventID: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", OccurredAt: time.Now().UTC(), SpaceID: events.SystemSpaceID, SubjectID: validHostMetric().GetHostId()})
	require.NoError(t, err)
	return encoded.Message
}

func TestValidateHostMetricContract(t *testing.T) {
	message := validHostMessage(t)
	metric, err := ValidateMessage(message)
	require.NoError(t, err)
	assert.Equal(t, message.GetSubjectId(), metric.GetHostId())
	message.SpaceId = "crypto"
	assert.Error(t, func() error { _, err := ValidateMessage(message); return err }())
}

// 正常路径：登记在线、更新最新快照、写本地历史，历史能按时间读回。
func TestStorePersistsSnapshotLocally(t *testing.T) {
	db := openRegistryTestDB(t)
	store := NewStore(NewSampleStore(db))
	store.SetRegistry(NewRegistry(db))
	message := validHostMessage(t)
	metric, err := ValidateMessage(message)
	require.NoError(t, err)
	require.NoError(t, store.Persist(context.Background(), message, metric))
	require.NoError(t, store.Persist(context.Background(), message, metric), "同一分钟重放覆盖，不报错")

	agents, err := store.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "host", agents[0].Hostname)
	require.NotNil(t, agents[0].Snapshot)

	occurred := message.GetOccurredAt().AsTime().UTC()
	history, err := store.History(context.Background(), metric.GetHostId(), occurred.Add(-time.Hour), occurred.Add(time.Hour), 0)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, occurred.Truncate(time.Minute).Format(time.RFC3339Nano), history[0].ObservedAt)
	assert.EqualValues(t, 25, history[0].Snapshot.GetCpu().GetUsagePercent())
}

// 本地历史写不进去时：最近一次上报照常登记（在线判断与阈值检查都靠它和最新快照），消息报错等待重投。
func TestStoreKeepsPresenceWhenHistoryWriteFails(t *testing.T) {
	db := openRegistryTestDB(t)
	store := NewStore(NewSampleStore(db))
	store.SetRegistry(NewRegistry(db))
	require.NoError(t, db.Exec("DROP TABLE t_monitor_host_samples").Error)

	message := validHostMessage(t)
	metric, err := ValidateMessage(message)
	require.NoError(t, err)
	require.Error(t, store.Persist(context.Background(), message, metric))
	require.Error(t, store.Persist(context.Background(), message, metric), "重投同一条消息")

	agents, err := store.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.True(t, agents[0].Reachable)
	assert.EqualValues(t, 25, agents[0].Snapshot.GetCpu().GetUsagePercent(), "最新快照已更新")
}

// 超过条数时在整个范围内等间隔抽取（保留首尾）；进程排行与采集器状态不进历史；按采样时间清理。
func TestSampleStoreHistorySpansTheRangeAndPrunes(t *testing.T) {
	samples := NewSampleStore(openRegistryTestDB(t))
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		snapshot := &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1, UsagePercent: float64(i)}, Memory: &hostmetricpb.MemoryMetric{},
			TopProcesses: []*hostmetricpb.ProcessMetric{{Pid: 1, Name: "storage"}}, Collectors: []*hostmetricpb.CollectorStatus{{}}}
		require.NoError(t, samples.Write(context.Background(), "control", base.Add(time.Duration(i)*time.Minute+10*time.Second), snapshot))
	}
	require.NoError(t, samples.Write(context.Background(), "storage", base, &hostmetricpb.HostSnapshot{}))

	history, err := samples.History(context.Background(), "control", base, base.Add(time.Hour), 3)
	require.NoError(t, err)
	require.Len(t, history, 3)
	for i, point := range history {
		assert.Equal(t, base.Add(time.Duration(2*i)*time.Minute).Format(time.RFC3339Nano), point.ObservedAt)
		assert.EqualValues(t, 2*i, point.Snapshot.GetCpu().GetUsagePercent())
		assert.Empty(t, point.Snapshot.GetTopProcesses())
		assert.Empty(t, point.Snapshot.GetCollectors())
	}
	all, err := samples.History(context.Background(), "control", base, base.Add(time.Hour), 0)
	require.NoError(t, err)
	assert.Len(t, all, 5, "不超过条数时原样返回")

	deleted, err := samples.DeleteBefore(context.Background(), base.Add(2*time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 3, deleted, "control 的前两分钟加上 storage 的一行")
	history, err = samples.History(context.Background(), "control", base, base.Add(time.Hour), 0)
	require.NoError(t, err)
	assert.Len(t, history, 3)
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

func validCPUMetric() *hostmetricpb.CpuMetric {
	return &hostmetricpb.CpuMetric{LogicalCores: 2, UsageAvailable: true, UsagePercent: 50}
}

func validMemoryMetric() *hostmetricpb.MemoryMetric {
	return &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40}
}
