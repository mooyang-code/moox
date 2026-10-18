package observability

import (
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/stretchr/testify/require"
)

// control 网关心跳里的会话转换成挂在出站主机上的信号；control 心跳过期时只能给出 unknown。
func TestRelaySignalsFollowControlHeartbeat(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	row := domain.GatewayObservation{HostID: "control", ObservedAt: &now, HostEnabledAt: now.Add(-time.Hour), ExpectedHash: "h", AppliedHash: "h"}
	status := &adminpb.HostGatewayRuntimeStatus{InstanceId: "c", LastSeenAt: now.Format(time.RFC3339), RelaySessions: []*adminpb.RelaySessionStatus{
		{HostId: "home", Online: true, InstanceId: "i", ConnectedAt: now.Format(time.RFC3339), Reconnects: 1},
		{HostId: "lab"},
	}}
	relay := func(signals []GatewaySignal) map[string]string {
		result := map[string]string{}
		for _, signal := range signals {
			if signal.Kind == RelaySignalKind {
				result[signal.HostID] = signal.Status
			}
		}
		return result
	}
	signals := gatewayObservationSignals(row, status, now)
	require.Equal(t, map[string]string{"home": "healthy", "lab": "down"}, relay(signals))
	require.Equal(t, "heartbeat", signals[0].Kind, "主机网关自身的信号顺序不变")

	stale := now.Add(3 * time.Minute)
	require.Equal(t, map[string]string{"home": "unknown", "lab": "unknown"}, relay(gatewayObservationSignals(row, status, stale)))
	row.ReadError = "GetHostRoutes: connection refused"
	require.Equal(t, map[string]string{"home": "unknown", "lab": "unknown"}, relay(gatewayObservationSignals(row, status, now)))
}
