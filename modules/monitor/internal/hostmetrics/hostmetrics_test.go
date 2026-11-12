package hostmetrics

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorstore "github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHostMetric() *hostmetricpb.HostMetric {
	return &hostmetricpb.HostMetric{AgentId: "aB3x", Hostname: "host", BootId: "boot", AgentVersion: "test", Snapshot: &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50},
	}}
}

func validHostMessage(t *testing.T) *eventpb.EventMessage {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.ObservabilityHostSnapshotReported, validHostMetric(), events.PublishOptions{EventID: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", OccurredAt: time.Now().UTC(), SpaceID: events.SystemSpaceID, SubjectID: validHostMetric().GetAgentId()})
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
	history, err := store.History(context.Background(), metric.GetAgentId(), occurred.Add(-time.Hour), occurred.Add(time.Hour), 0)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, occurred.Truncate(time.Minute).Format(time.RFC3339Nano), history[0].ObservedAt)
	assert.EqualValues(t, 25, history[0].Snapshot.GetCpu().GetUsagePercent())
}

// 本地历史写不进去时：在线登记和阈值评估照常发生，消息报错等待重投；重投不会重复评估。
func TestStoreKeepsPresenceAndAlertsWhenHistoryWriteFails(t *testing.T) {
	db := openRegistryTestDB(t)
	repos := monitorstore.NewRepositories(db)
	agentID := validHostMetric().GetAgentId()
	checkID := HostRuleKey(agentID, HostMetricCPU)
	require.NoError(t, repos.Alerts.CreateRule(context.Background(), &domain.AlertRule{
		SpaceID: events.SystemSpaceID, RuleID: "default:" + checkID, CheckID: checkID, Enabled: true,
		FailureThreshold: 2, SuccessThreshold: 1, Description: `{"threshold":20}`,
	}))
	cache, err := NewRuleCache(RuleCacheOptions{Repository: repos.Alerts})
	require.NoError(t, err)
	require.NoError(t, cache.Start(t.Context()))

	store := NewStore(NewSampleStore(db))
	store.SetRegistry(NewRegistry(db))
	store.SetAlertEvaluator(&AlertEvaluator{Cache: cache, Repository: repos.Alerts})
	require.NoError(t, db.Exec("DROP TABLE t_monitor_host_samples").Error)

	message := validHostMessage(t)
	metric, err := ValidateMessage(message)
	require.NoError(t, err)
	require.Error(t, store.Persist(context.Background(), message, metric))
	require.Error(t, store.Persist(context.Background(), message, metric), "重投同一条消息")

	restarted := NewStore(nil)
	restarted.SetRegistry(NewRegistry(db))
	agents, err := restarted.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.True(t, agents[0].Reachable)

	state, err := repos.Alerts.GetState(context.Background(), events.SystemSpaceID, "default:"+checkID, checkID)
	require.NoError(t, err)
	assert.Equal(t, 1, state.FailureCount, "同一条消息只评估一次")
}

func TestSampleStoreHistoryReturnsLatestRowsAscendingAndPrunes(t *testing.T) {
	samples := NewSampleStore(openRegistryTestDB(t))
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		snapshot := &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1, UsagePercent: float64(i)}, Memory: &hostmetricpb.MemoryMetric{}}
		require.NoError(t, samples.Write(context.Background(), "agent", base.Add(time.Duration(i)*time.Minute+10*time.Second), snapshot))
	}
	require.NoError(t, samples.Write(context.Background(), "other", base, &hostmetricpb.HostSnapshot{}))

	history, err := samples.History(context.Background(), "agent", base, base.Add(time.Hour), 3)
	require.NoError(t, err)
	require.Len(t, history, 3)
	for i, point := range history {
		assert.Equal(t, base.Add(time.Duration(i+2)*time.Minute).Format(time.RFC3339Nano), point.ObservedAt)
		assert.EqualValues(t, i+2, point.Snapshot.GetCpu().GetUsagePercent())
	}

	deleted, err := samples.DeleteBefore(context.Background(), base.Add(2*time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 3, deleted, "agent 的前两分钟加上 other 的一行")
	history, err = samples.History(context.Background(), "agent", base, base.Add(time.Hour), 0)
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

func TestHostHelpers(t *testing.T) {
	assert.Equal(t, 1, positive(0))
	assert.Equal(t, 3, positive(3))
	first := deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered)
	assert.Equal(t, first, deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered))
	assert.NotEqual(t, first, deterministicEventID("msg-2", "rule-1", domain.AlertEventTriggered))
	threshold, recovery := RuleThresholds(domain.AlertRule{})
	assert.Equal(t, 80.0, threshold)
	assert.Equal(t, 80.0, recovery)
	threshold, recovery = RuleThresholds(domain.AlertRule{Description: `{"threshold":90,"recovery_threshold":85}`})
	assert.Equal(t, 90.0, threshold)
	assert.Equal(t, 85.0, recovery)
}

func validCPUMetric() *hostmetricpb.CpuMetric {
	return &hostmetricpb.CpuMetric{LogicalCores: 2, UsageAvailable: true, UsagePercent: 50}
}

func validMemoryMetric() *hostmetricpb.MemoryMetric {
	return &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 40, AvailableBytes: 60, UsagePercent: 40}
}
