package bootstrap

import (
	"testing"
	"time"

	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/stretchr/testify/require"
)

// 反向连接断开满 3 分钟才判定失败；期间与无法确认时保持原状态；恢复后清除计时。
func TestRelayCheckAlertsAfterThreeMinutes(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	since := map[string]time.Time{}
	down := monitorobservability.GatewaySignal{HostID: "home", Kind: monitorobservability.RelaySignalKind, Status: "down", Reason: "主机网关与控制主机的反向连接已断开"}
	_, frozen := relayItem(down, since, now)
	require.True(t, frozen)
	_, frozen = relayItem(down, since, now.Add(relayAlertAfter-time.Second))
	require.True(t, frozen)
	item, frozen := relayItem(down, since, now.Add(relayAlertAfter))
	require.False(t, frozen)
	require.False(t, item.success)
	require.Equal(t, "gateway:home:relay", item.checkID)
	require.Equal(t, "主机网关反向连接（home）", item.name)
	require.Contains(t, item.reason, "host-agent")

	unknown := down
	unknown.Status = "unknown"
	_, frozen = relayItem(unknown, since, now.Add(time.Hour))
	require.True(t, frozen)

	healthy := down
	healthy.Status = "healthy"
	item, frozen = relayItem(healthy, since, now.Add(time.Hour))
	require.False(t, frozen)
	require.True(t, item.success)
	require.Empty(t, since, "恢复后重新计时")
}
