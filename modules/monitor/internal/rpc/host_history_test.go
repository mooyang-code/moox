package rpc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/require"
)

// 主机历史只读本地表：可查最近 7 天；请求超出这个范围时截掉并置 data_gap，没有快照时也置 data_gap。
func TestQueryHostMetricHistoryClampsToRetention(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	samples, err := store.WithDatabase(mgr, hostmetrics.NewSampleStore)
	require.NoError(t, err)
	now := time.Now().UTC()
	// 8 天前（已超出保留期，清理前仍在表里）、6 天前、半小时前各一份。
	for _, at := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(-6 * 24 * time.Hour), now.Add(-30 * time.Minute)} {
		require.NoError(t, samples.Write(t.Context(), "agent-a", at, &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1}}))
	}
	service := New(mgr.Repositories(), Options{HostStore: hostmetrics.NewStore(samples)})

	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	for _, tc := range []struct {
		name       string
		start, end string
		points     int
		gap        bool
	}{
		{name: "7 天内", start: ts(-7*24*time.Hour + time.Minute), end: ts(-time.Minute), points: 2},
		{name: "页面的 7 天选项按整 7 天请求，不算超出", start: ts(-7 * 24 * time.Hour), end: ts(0), points: 2},
		{name: "起点早于 7 天被截掉", start: ts(-10 * 24 * time.Hour), end: ts(-time.Minute), points: 2, gap: true},
		{name: "整段都早于 7 天", start: ts(-10 * 24 * time.Hour), end: ts(-8*24*time.Hour + time.Minute), points: 0, gap: true},
		{name: "范围内没有快照", start: ts(-5 * 24 * time.Hour), end: ts(-4 * 24 * time.Hour), points: 0, gap: true},
		{name: "默认最近 1 小时", points: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rsp, err := service.QueryHostMetricHistory(t.Context(), &monitorpb.QueryHostMetricHistoryReq{AgentId: "agent-a", StartAt: tc.start, EndAt: tc.end})
			require.NoError(t, err)
			require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
			require.Len(t, rsp.GetPoints(), tc.points)
			require.Equal(t, tc.gap, rsp.GetDataGap())
		})
	}

	rsp, err := service.QueryHostMetricHistory(t.Context(), &monitorpb.QueryHostMetricHistoryReq{AgentId: "agent-a", StartAt: ts(-time.Minute), EndAt: ts(-time.Hour)})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
