package snapshot

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// control 快照带出站主机的中转许可；目录与许可不一致、端口不在服务目录、健康端口对 Monitor 以外开放都拒绝。
func TestSnapshotRelayAllowlist(t *testing.T) {
	raw := testsnapshot.NewWithOutbound(t, "control", "home")
	view, err := Build("control", raw)
	require.NoError(t, err)
	require.True(t, view.OutboundHost("home"))
	require.False(t, view.OutboundHost("control"))
	relay, ok := view.Relay("home")
	require.True(t, ok)
	require.True(t, relay.Allowed("monitor", servicecatalog.HostGatewayRemotePort))
	require.Greater(t, len(relay.Ports), 1, "主机级组件的健康端口")

	// 其他主机拿到的目录里有中转主机，但没有许可。
	storage, err := Build("storage", testsnapshot.NewWithOutbound(t, "storage", "home", "storage-primary"))
	require.NoError(t, err)
	_, ok = storage.Relay("home")
	require.False(t, ok)
	require.False(t, storage.OutboundHost("home"), "不是由本机中转")

	healthPort := raw.Relays[0].Ports[1].Port
	for name, mutate := range map[string]func(*pb.HostGatewaySnapshot){
		"missing relay":     func(s *pb.HostGatewaySnapshot) { s.Relays = nil },
		"direct host relay": func(s *pb.HostGatewaySnapshot) { s.Relays[0].HostId = "control" },
		"duplicate relay": func(s *pb.HostGatewaySnapshot) {
			s.Relays = append(s.Relays, proto.Clone(s.Relays[0]).(*pb.HostGatewayRelay))
		},
		"arbitrary port":      func(s *pb.HostGatewaySnapshot) { s.Relays[0].Ports[1].Port = 22 },
		"health port caller":  func(s *pb.HostGatewaySnapshot) { s.Relays[0].Ports[1].Callers = []string{"admin"} },
		"duplicate port":      func(s *pb.HostGatewaySnapshot) { s.Relays[0].Ports[0].Port = healthPort },
		"duplicate caller":    func(s *pb.HostGatewaySnapshot) { s.Relays[0].Ports[0].Callers = []string{"monitor", "monitor"} },
		"relay on other host": func(s *pb.HostGatewaySnapshot) { s.HostId = "home" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := proto.Clone(raw).(*pb.HostGatewaySnapshot)
			mutate(changed)
			testsnapshot.Rehash(t, changed)
			_, err := Build(changed.HostId, changed)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}
