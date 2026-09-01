package rpc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSCFEnvironmentBytesIsStable(t *testing.T) {
	values := map[string]string{"B": "2", "A": "1"}
	require.Equal(t, len("A=1\x00")+len("B=2\x00"), scfEnvironmentBytes(values))
}

func TestManagedEnvironmentRejectsUnknownKey(t *testing.T) {
	if _, ok := managedEnvironmentKeys["TENCENTCLOUD_SECRET_KEY"]; ok {
		t.Fatal("provider credentials must not be collector-managed")
	}
}

func TestManagedEnvironmentAcceptsMarketIdentityKeys(t *testing.T) {
	for _, key := range []string{
		"MOOX_MARKET_FETCH_MARKET_ID",
		"MOOX_MARKET_FETCH_INSTRUMENT_TYPE",
		"MOOX_MARKET_FETCH_SOURCE_ID",
		"MOOX_MARKET_FETCH_SERIES_TAG",
	} {
		_, ok := managedEnvironmentKeys[key]
		require.True(t, ok, "missing managed environment key %s", key)
	}
}

func TestManagedEnvironmentAllowsStockCNRouteKeys(t *testing.T) {
	for _, key := range []string{
		"MOOX_MARKET_FETCH_PROVIDER_CHAIN",
		"MOOX_MARKET_FETCH_ROUTE_VERSION",
		"MOOX_MARKET_FETCH_GROUP_ID",
		"MOOX_MARKET_FETCH_GROUP_COUNT",
		"MOOX_METRICS_EVENTBUS_URL",
		"MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE",
	} {
		_, ok := managedEnvironmentKeys[key]
		require.True(t, ok, "missing managed environment key %s", key)
	}
}

func TestSupportedTimerCronAllowsSecondOffsets(t *testing.T) {
	for _, cron := range []string{
		"0 * * * * * *",
		"17 * * * * * *",
		"59 */5 * * * * *",
		"23 0 0 * * * *",
	} {
		require.True(t, isSupportedTimerCron(cron), "cron=%q", cron)
	}
	for _, cron := range []string{
		"60 * * * * * *",
		"-1 * * * * * *",
		"*/5 * * * * * *",
		"17 */2 * * * * *",
	} {
		require.False(t, isSupportedTimerCron(cron), "cron=%q", cron)
	}
}

func TestManagedEnvironmentMatchesOnlyCollectorOwnedValues(t *testing.T) {
	current := map[string]string{"MOOX_MARKET_FETCH_SUBJECTS": "BTC-USDT", "SECRET": "keep"}
	require.True(t, managedEnvironmentMatches(current, map[string]string{"MOOX_MARKET_FETCH_SUBJECTS": "BTC-USDT"}))
	require.False(t, managedEnvironmentMatches(current, map[string]string{"MOOX_MARKET_FETCH_SUBJECTS": "ETH-USDT"}))
	require.False(t, managedEnvironmentMatches(current, map[string]string{"MOOX_MARKET_FETCH_DNS_HASH": "missing"}))
}
