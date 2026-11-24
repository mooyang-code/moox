package hostmetrics

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/require"
)

// 进程排行里没有名字的条目忽略；CPU 不是有限数的条目保留，但按“没有 CPU 值”记录（NaN 进不了 JSON）。
func TestTopProcessesIgnoresUnusableEntries(t *testing.T) {
	processes := TopProcesses(&hostmetricpb.HostSnapshot{TopProcesses: []*hostmetricpb.ProcessMetric{
		{Pid: 11, Name: "storage-primary", CpuPercent: 340, CpuAvailable: true, RssBytes: 5 << 30},
		{Pid: 14, Name: " ", CpuPercent: 900, CpuAvailable: true},
		{Pid: 15, Name: "broken", CpuPercent: math.NaN(), CpuAvailable: true, RssBytes: 1 << 20},
	}})
	require.Len(t, processes, 2)
	require.EqualValues(t, 11, processes[0].PID)
	require.True(t, processes[0].CPUAvailable)
	require.Equal(t, "broken", processes[1].Name)
	require.False(t, processes[1].CPUAvailable)
}

func TestCheckIDRoundTrip(t *testing.T) {
	for _, kind := range append([]string{CheckKindPresence, CheckKindAgent}, Metrics...) {
		agent, parsed, ok := ParseCheckID(CheckID("control", kind))
		require.True(t, ok, kind)
		require.Equal(t, "control", agent)
		require.Equal(t, kind, parsed)
	}
	for _, id := range []string{"host::cpu", "host:control:unknown", "other:control:cpu", "host:control", "host:control:cpu:extra"} {
		_, _, ok := ParseCheckID(id)
		require.False(t, ok, id)
	}
}

// 每个有阈值的指标都要有告警线、不高于告警线的恢复线和连续采样次数。
func TestEveryMetricHasAThreshold(t *testing.T) {
	for _, metric := range Metrics {
		threshold := MetricThreshold(metric)
		require.Positive(t, threshold.Samples, metric)
		require.Positive(t, threshold.Recovery, metric)
		require.LessOrEqual(t, threshold.Recovery, threshold.Alert, metric)
		require.NotEmpty(t, MetricLabel(metric), metric)
	}
	require.Zero(t, MetricThreshold(CheckKindPresence))
}
