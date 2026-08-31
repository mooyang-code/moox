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
		if _, ok := managedEnvironmentKeys[key]; !ok {
			t.Fatalf("market identity key %q is not managed", key)
		}
	}
}

func TestManagedEnvironmentMatchesOnlyCollectorOwnedValues(t *testing.T) {
	current := map[string]string{"MOOX_MARKET_FETCH_SUBJECTS": "BTC-USDT", "SECRET": "keep"}
	require.True(t, managedEnvironmentMatches(current, map[string]string{"MOOX_MARKET_FETCH_SUBJECTS": "BTC-USDT"}))
	require.False(t, managedEnvironmentMatches(current, map[string]string{"MOOX_MARKET_FETCH_SUBJECTS": "ETH-USDT"}))
	require.False(t, managedEnvironmentMatches(current, map[string]string{"MOOX_MARKET_FETCH_DNS_HASH": "missing"}))
}
