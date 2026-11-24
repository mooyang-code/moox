package snapshot

import (
	"testing"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

const (
	gatewayHealthPort = 11012 // host-gateway 的健康端口
	archiveDataPort   = 11428 // archive-worker 的 relay_http 数据端口
)

// 中转许可不随快照下发，control 按拓扑与内嵌目录自己算：出站主机的跨主机端口对能调它任一服务的调用方开放；
// 组件健康端口、随便一个端口都不经中转。别的主机没有许可。
func TestRelayAllowlistIsComputedOnControl(t *testing.T) {
	view, err := Build("control", testsnapshot.NewWithOutbound(t, "control", "home"))
	require.NoError(t, err)
	require.True(t, view.OutboundHost("home"))
	require.False(t, view.OutboundHost("control"))
	require.False(t, view.OutboundHost("elsewhere"))
	relay, ok := view.Relay("home")
	require.True(t, ok)
	require.Equal(t, "home", relay.HostID)
	require.Len(t, relay.Ports, 1, "只有网关跨主机端口，组件健康端口不经中转")
	require.Equal(t, servicecatalog.HostGatewayRemotePort, relay.Ports[0].Port)
	// 出站主机上只有主机级组件：能调它们的 monitor 与 moox-cli 可以打开，调不到它任何服务的 trade 与没登记的名字不行。
	require.True(t, relay.Allowed("monitor", servicecatalog.HostGatewayRemotePort))
	require.True(t, relay.Allowed("moox-cli", servicecatalog.HostGatewayRemotePort))
	require.False(t, relay.Allowed("trade", servicecatalog.HostGatewayRemotePort))
	require.False(t, relay.Allowed("nobody", servicecatalog.HostGatewayRemotePort))
	for _, port := range []int{22, gatewayHealthPort, archiveDataPort} {
		require.False(t, relay.Allowed("monitor", port), port)
		require.False(t, relay.Allowed("console", port), port)
	}
	// 可直达的主机、没登记的主机没有许可。
	_, ok = view.Relay("control")
	require.False(t, ok)
	_, ok = view.Relay("elsewhere")
	require.False(t, ok)

	// 跨主机端口的调用方跟着出站主机上的部署走：storage-primary 放过去以后，调它的 collector 才能打开。
	require.False(t, relay.Allowed("collector", servicecatalog.HostGatewayRemotePort))
	raw := testsnapshot.NewWithOutbound(t, "control", "home")
	testsnapshot.Deployment(t, raw, "control", "storage-primary").HostId = "home"
	testsnapshot.Rehash(t, raw)
	view, err = Build("control", raw)
	require.NoError(t, err)
	relay, ok = view.Relay("home")
	require.True(t, ok)
	require.True(t, relay.Allowed("collector", servicecatalog.HostGatewayRemotePort))
	require.Len(t, relay.Ports, 1)
}

// 非 control 主机拿到同一份快照，目录里看得到出站主机经 control 中转，但自己没有中转许可。
func TestRelayAllowlistOnlyOnControl(t *testing.T) {
	storage, err := Build("storage", testsnapshot.NewWithOutbound(t, "storage", "home", "storage-primary"))
	require.NoError(t, err)
	_, ok := storage.Relay("home")
	require.False(t, ok)
	require.False(t, storage.OutboundHost("home"), "不是由本机中转")
	require.Equal(t, "control", storage.Directory().Hosts["home"].Relay)

	// 同一份快照，出站主机自己与 control 各算各的：只有 control 有许可。
	raw := testsnapshot.NewWithOutbound(t, "home", "home")
	home, err := Build("home", raw)
	require.NoError(t, err)
	_, ok = home.Relay("home")
	require.False(t, ok)
	require.False(t, home.OutboundHost("home"))
	control, err := Build("control", raw)
	require.NoError(t, err)
	_, ok = control.Relay("home")
	require.True(t, ok)
}

// 归档 worker 部署在出站主机上时，它的数据端口只对 console（下载与预览）与 moox-cli（导入写入）开放，monitor 打不开。
func TestRelayDataPortOnlyForConsoleAndCLI(t *testing.T) {
	raw := testsnapshot.NewWithOutbound(t, "control", "home")
	testsnapshot.Deployment(t, raw, "control", "archive-worker").HostId = "home"
	testsnapshot.Rehash(t, raw)
	view, err := Build("control", raw)
	require.NoError(t, err)
	relay, ok := view.Relay("home")
	require.True(t, ok)
	require.True(t, relay.Allowed("console", archiveDataPort))
	require.False(t, relay.Allowed("monitor", archiveDataPort))
	require.True(t, relay.Allowed("moox-cli", archiveDataPort))
	require.True(t, relay.Allowed("monitor", servicecatalog.HostGatewayRemotePort))
	require.Len(t, relay.Ports, 2)

	// 归档 worker 被停用后数据端口不再开放。
	testsnapshot.Deployment(t, raw, "home", "archive-worker").Enabled = false
	testsnapshot.Rehash(t, raw)
	view, err = Build("control", raw)
	require.NoError(t, err)
	relay, ok = view.Relay("home")
	require.True(t, ok)
	require.False(t, relay.Allowed("console", archiveDataPort))
}

// 出站主机被停用：不在目录里，也没有中转许可。
func TestDisabledOutboundHostHasNoRelay(t *testing.T) {
	raw := testsnapshot.NewWithOutbound(t, "control", "home")
	testsnapshot.Host(t, raw, "home").Enabled = false
	testsnapshot.Rehash(t, raw)
	view, err := Build("control", raw)
	require.NoError(t, err)
	_, ok := view.Relay("home")
	require.False(t, ok)
	require.False(t, view.OutboundHost("home"))
}
