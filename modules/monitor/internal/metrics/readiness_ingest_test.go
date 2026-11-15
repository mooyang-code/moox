package metrics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 上报里的就绪与生命周期只跟着更新的上报走（重投递的旧上报不覆盖）；管理端代报的外部程序各记一行，时间取最近心跳；
// 反复重启按启动标识的变化数（每个启动标识第一次上报的时间落在窗口里，前面还有更早的启动才算一次变化）。
func TestIngestRecordsReadinessLifecycleDelegatesAndBoots(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(mgr, NewMetricMessageStore)
	require.NoError(t, err)
	catalog := NewCatalog(messages)
	now := time.Now().UTC().Truncate(time.Second)
	report := func(id string, at time.Time, boot string, ready bool, stage string) {
		_, err := messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: id, OccurredAt: timestamppb.New(at)}, &metricspb.MetricReport{
			ServiceName: "factor-mgr", InstanceId: "factor-mgr@control", NodeId: "control", BootId: boot,
			Ready: proto.Bool(ready), NotReadyReason: map[bool]string{false: "数据库不可用"}[ready],
			Lifecycle: &metricspb.ModuleLifecycle{Stage: stage},
			Delegated: []*metricspb.DelegatedReport{
				{ServiceName: "factor-engine", InstanceId: "engine@mac", BootId: "engine-1", LastHeartbeatUnix: at.Add(-5 * time.Second).Unix(), Ready: proto.Bool(true)},
				{ServiceName: "archive-worker", InstanceId: "worker@mac"},
			},
		}, nil)
		require.NoError(t, err)
	}
	report("m1", now.Add(-time.Minute), "boot-1", true, "running")
	report("m2", now, "boot-1", false, "paused")
	report("m0", now.Add(-2*time.Minute), "boot-1", true, "running") // 重投递的旧上报
	rows, _, err := catalog.ListServicesAt(t.Context(), "", 0, 10, now)
	require.NoError(t, err)
	byService := map[string]MetricService{}
	for _, row := range rows {
		byService[row.ServiceName] = row
	}
	mgrRow := byService["factor-mgr"]
	require.Equal(t, ReadinessNotReady, mgrRow.Readiness)
	require.Equal(t, "数据库不可用", mgrRow.NotReadyReason)
	require.Equal(t, "paused", DecodeLifecycle(mgrRow.LifecycleJSON).GetStage(), "旧上报不覆盖新的生命周期")
	engine := byService["factor-engine"]
	require.Empty(t, engine.NodeID, "代报行不带主机，由概览按组件名对到部署")
	require.Equal(t, ReadinessReady, engine.Readiness)
	require.WithinDuration(t, now.Add(-5*time.Second), engine.LastSeenAt, time.Second)
	require.NotContains(t, byService, "archive-worker", "从没心跳过的外部程序不记，按从未上报处理")

	report("m3", now, "boot-2", true, "running")
	report("m4", now, "boot-3", true, "running")
	changes, err := catalog.BootChanges(t.Context(), now.Add(-RestartWindow))
	require.NoError(t, err)
	require.Equal(t, 2, changes["control\x00factor-mgr"], "全新部署：三个启动标识是两次变化")
	require.NotContains(t, changes, "\x00factor-engine", "只启动过一次的没有变化")
	changes, err = catalog.BootChanges(t.Context(), now.Add(-30*time.Second))
	require.NoError(t, err)
	require.Equal(t, 2, changes["control\x00factor-mgr"], "窗口之前已有启动：窗口里的两个新启动标识各算一次变化")
}
