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
	stats := gatewayclient.PoolStats{RequestDialsOK: 3, RequestDialsErr: 1, WarmupDialsOK: 20, WarmupDialsErr: 2, ConnectRetriesRecovered: 4, ConnectRetriesFailed: 1}
	require.NoError(t, RegisterPoolMetrics(registry, func() gatewayclient.PoolStats { return stats }))
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP moox_gatewayclient_connect_retries_total Calls retried after a connection could not be obtained (request not sent), by outcome.
# TYPE moox_gatewayclient_connect_retries_total counter
moox_gatewayclient_connect_retries_total{outcome="failed"} 1
moox_gatewayclient_connect_retries_total{outcome="recovered"} 4
# HELP moox_gatewayclient_pool_dials_total Gateway client connection dials by source (request path or pool warm-up) and result.
# TYPE moox_gatewayclient_pool_dials_total counter
moox_gatewayclient_pool_dials_total{result="error",source="request"} 1
moox_gatewayclient_pool_dials_total{result="error",source="warmup"} 2
moox_gatewayclient_pool_dials_total{result="ok",source="request"} 3
moox_gatewayclient_pool_dials_total{result="ok",source="warmup"} 20
`)))
	stats.RequestDialsOK = 5
	require.Equal(t, 6, testutil.CollectAndCount(registry), "每次采集读取最新计数")
}
