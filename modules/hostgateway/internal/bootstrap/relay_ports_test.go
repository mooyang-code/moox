package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

// 出站主机只把中转流接到网关跨主机端口、组件健康端口与归档 worker 的数据端口，其余端口一律拒绝。
func TestRelayLocalPortsAllowRelayHTTPPort(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	allowed := relayLocalPorts(catalog, "11003")
	for _, port := range []int{11003, 11418, 11417, 11428} {
		require.True(t, allowed(port), "port %d", port)
	}
	for _, port := range []int{22, 11473, 11429} {
		require.False(t, allowed(port), "port %d", port)
	}
}
