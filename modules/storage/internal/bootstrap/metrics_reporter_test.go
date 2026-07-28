package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsReporterSpecUsesDistinctStorageRoleIdentity(t *testing.T) {
	primary, err := MetricsReporterSpecForRole("primary")
	require.NoError(t, err)
	require.Equal(t, "storage_primary", primary.ServiceName)
	require.Equal(t, "trpc.moox.storage.primary.metrics.timer", primary.TimerService)

	view, err := MetricsReporterSpecForRole("view")
	require.NoError(t, err)
	require.Equal(t, "storage_view", view.ServiceName)
	require.Equal(t, "trpc.moox.storage.view.metrics.timer", view.TimerService)
	require.NotEqual(t, primary.ServiceName, view.ServiceName)

	_, err = MetricsReporterSpecForRole("node")
	require.Error(t, err)
}
