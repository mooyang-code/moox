package bootstrap

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

// 失联原因：网关心跳也停 → “主机失联”（严重）；只有 HostAgent 停止 → “host-agent 停止上报”（警告），恢复标题与之对应。
func TestPresenceDistinguishesHostLossFromAgentStop(t *testing.T) {
	gatewayUp := map[string]bool{"storage": true}
	gate := newPresenceGate(func(context.Context, string) (string, error) { return "", nil })
	gate.gatewayAlive = func(_ context.Context, hostID string, _ time.Time) bool { return gatewayUp[hostID] }
	sender := &recordingPresenceSender{}
	sink := hostPresenceTransitionSinkWithGate(func(context.Context) (notification.Sender, error) { return sender, nil }, nil, gate)
	now := time.Now().UTC()
	send := func(hostID, agentID, to string) {
		from := hostmetrics.PresenceReachable
		if to == hostmetrics.PresenceReachable {
			from = hostmetrics.PresenceUnreachable
		}
		require.NoError(t, sink(t.Context(), hostmetrics.PresenceTransition{HostID: hostID, AgentID: agentID, Hostname: hostID, From: from, To: to, ObservedAt: now}))
	}

	send("storage", "agent-s", hostmetrics.PresenceUnreachable)
	send("compute-1", "agent-c", hostmetrics.PresenceUnreachable)
	gatewayUp["storage"] = false // 恢复时网关状态无关，按离线时的判断选标题
	send("storage", "agent-s", hostmetrics.PresenceReachable)
	send("compute-1", "agent-c", hostmetrics.PresenceReachable)

	require.Len(t, sender.messages, 4)
	require.Equal(t, "主机 storage · host-agent 停止上报", sender.messages[0].Title)
	require.Equal(t, notification.SeverityWarning, sender.messages[0].Severity)
	require.Contains(t, sender.messages[0].Body, "主机网关心跳正常")
	require.Equal(t, "主机 compute-1 · 失联", sender.messages[1].Title)
	require.Equal(t, notification.SeverityCritical, sender.messages[1].Severity)
	require.Contains(t, sender.messages[1].Body, "主机网关心跳也已停止")
	require.Equal(t, "主机 storage · host-agent 已恢复上报", sender.messages[2].Title)
	require.Equal(t, "主机 compute-1 · 已恢复在线", sender.messages[3].Title)
}

// 网关心跳判断：Monitor 缓存的网关状态要新鲜，且网关最近心跳在 2 分钟内；读不到、过期、读取出错都按“没有心跳”。
func TestGatewayHeartbeatAlive(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Now().UTC().Truncate(time.Second)
	status := func(seen time.Time) string {
		raw, err := json.Marshal(&adminpb.HostGatewayRuntimeStatus{LastSeenAt: seen.Format(time.RFC3339Nano)})
		require.NoError(t, err)
		return string(raw)
	}
	require.NoError(t, repos.Gateways.Reconcile(t.Context(), []domain.GatewayObservation{
		{HostID: "storage", StatusJSON: status(now.Add(-30 * time.Second))},
		{HostID: "compute-1", StatusJSON: status(now.Add(-5 * time.Minute))},
		{HostID: "broken", ReadError: "admin unavailable"},
	}, now))
	alive := gatewayHeartbeatAlive(repos.Gateways)
	require.True(t, alive(t.Context(), "storage", now))
	require.False(t, alive(t.Context(), "compute-1", now), "网关心跳已超过 2 分钟")
	require.False(t, alive(t.Context(), "broken", now), "Admin 状态读取失败")
	require.False(t, alive(t.Context(), "unknown", now), "没有登记")
	require.False(t, alive(t.Context(), "storage", now.Add(5*time.Minute)), "Monitor 缓存本身过期")
	require.False(t, gatewayHeartbeatAlive(nil)(t.Context(), "storage", now))
}
