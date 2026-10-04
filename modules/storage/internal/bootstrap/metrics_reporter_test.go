package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsReporterSpecUsesDistinctStorageRoleIdentity(t *testing.T) {
	primary, err := MetricsReporterSpecForRole("primary")
	require.NoError(t, err)
	require.Equal(t, "storage-primary", primary.ServiceName)
	require.Equal(t, "trpc.moox.storage.primary.metrics.timer", primary.TimerService)

	view, err := MetricsReporterSpecForRole("view")
	require.NoError(t, err)
	require.Equal(t, "storage-view", view.ServiceName)
	require.Equal(t, "trpc.moox.storage.view.metrics.timer", view.TimerService)
	require.NotEqual(t, primary.ServiceName, view.ServiceName)

	node, err := MetricsReporterSpecForRole("node")
	require.NoError(t, err)
	require.Equal(t, "storage-node", node.ServiceName)
	require.Equal(t, "trpc.moox.storage.node.metrics.timer", node.TimerService)
	require.NotEqual(t, view.ServiceName, node.ServiceName)
}
