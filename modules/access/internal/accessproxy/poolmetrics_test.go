package accessproxy

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestPoolMetricsExposeDialAndRetryCounters(t *testing.T) {
	registry := prometheus.NewRegistry()
	stats := gatewayclient.PoolStats{DialsOK: 23, DialsErr: 3, WarmupOK: 20, WarmupErr: 2, ConnectRetriesRecovered: 4, ConnectRetriesFailed: 1}
	require.NoError(t, RegisterPoolMetrics(registry, func() gatewayclient.PoolStats { return stats }))
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP moox_gatewayclient_connect_retries_total Calls retried after a connection could not be obtained (request not sent), by outcome.
# TYPE moox_gatewayclient_connect_retries_total counter
moox_gatewayclient_connect_retries_total{outcome="failed"} 1
moox_gatewayclient_connect_retries_total{outcome="recovered"} 4
# HELP moox_gatewayclient_pool_dials_total Gateway client connections dialed (request path and warm-up together), by result.
# TYPE moox_gatewayclient_pool_dials_total counter
moox_gatewayclient_pool_dials_total{result="error"} 3
moox_gatewayclient_pool_dials_total{result="ok"} 23
# HELP moox_gatewayclient_pool_warmups_total Connections obtained by the pool warm-up routine, by result.
# TYPE moox_gatewayclient_pool_warmups_total counter
moox_gatewayclient_pool_warmups_total{result="error"} 2
moox_gatewayclient_pool_warmups_total{result="ok"} 20
`)))
	stats.DialsOK = 5
	require.Equal(t, 6, testutil.CollectAndCount(registry), "每次采集读取最新计数")
}
