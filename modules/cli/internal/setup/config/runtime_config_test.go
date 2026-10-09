package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderCollectorRuntimeConfigIncludesStockCapacity(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{
		SCFFetcher: SCFFetcher{Spaces: []SCFFetcherSpace{{
			SpaceID: "stockcn", TimerFunctionCount: 200, MeasuredSafeGroupSize: 30,
			StaggerStartSecond: DefaultStockCNStaggerStartSecond, StaggerWindowSeconds: DefaultStockCNStaggerWindowSeconds, StaggerMaxStartsPerSecond: DefaultStockCNStaggerMaxStartsPerSecond,
		}}},
	}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	stock := got["stockcn"].(map[string]any)
	require.Equal(t, 200, stock["expected_timer_function_count"])
	require.Equal(t, 30, stock["measured_safe_group_size"])
	require.Equal(t, 5, stock["stagger_start_second"])
	require.Equal(t, 35, stock["stagger_window_seconds"])
	require.Equal(t, 6, stock["stagger_max_starts_per_second"])
}

func TestRenderCollectorRuntimeConfigIncludesRetentionPolicy(t *testing.T) {
	snapshot := &Snapshot{Manifest: Manifest{CollectorRetention: CollectorRetention{
		MaintenanceInterval: "2m", MaintenanceOffset: "10s", MaintenanceTimeout: "30s", MaxRowsPerPass: 25000,
		ExecutionDetailRetention: "24h", ScheduledRunSummaryRetention: "720h",
		TerminalRetryRetention: "336h", PeriodSnapshotRetention: "720h",
	}}}
	rendered, err := RenderCollectorRuntimeConfig(snapshot, []byte("database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(rendered, &got))
	retention := got["collector_retention"].(map[string]any)
	require.Equal(t, "2m", retention["maintenance_interval"])
	require.Equal(t, "10s", retention["maintenance_offset"])
	require.Equal(t, "30s", retention["maintenance_timeout"])
	require.Equal(t, 25000, retention["max_rows_per_pass"])
	require.Equal(t, "24h", retention["execution_detail_retention"])
	require.Equal(t, "720h", retention["scheduled_run_summary_retention"])
	require.Equal(t, "336h", retention["terminal_retry_retention"])
	require.Equal(t, "720h", retention["period_snapshot_retention"])
}
