package dnscache

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/stretchr/testify/require"
)

func TestRefreshKeepsPreviousGoodRouteWhenOneDomainFails(t *testing.T) {
	cache := New(Config{Domains: []string{"api.binance.com", "fapi.binance.com"}})
	cache.lookup = func(_ context.Context, host string) ([]string, error) {
		if host == "api.binance.com" {
			return []string{"192.0.2.10"}, nil
		}
		return nil, context.DeadlineExceeded
	}
	require.NoError(t, cache.Refresh(context.Background()))

	cache.lookup = func(_ context.Context, host string) ([]string, error) {
		if host == "api.binance.com" {
			return nil, context.DeadlineExceeded
		}
		return []string{"192.0.2.20"}, nil
	}
	cache.lastAttempt = time.Time{}
	require.NoError(t, cache.Refresh(context.Background()))

	snapshot := cache.Snapshot()
	require.Equal(t, []string{"192.0.2.10"}, snapshot["api.binance.com"].IPs)
	require.Equal(t, []string{"192.0.2.20"}, snapshot["fapi.binance.com"].IPs)
}

func TestSnapshotReturnsDefensiveCopies(t *testing.T) {
	cache := New(Config{Domains: []string{"api.binance.com"}})
	cache.routes["api.binance.com"] = sources.DNSResolution{IPs: []string{"192.0.2.10"}}
	snapshot := cache.Snapshot()
	snapshot["api.binance.com"] = sources.DNSResolution{IPs: []string{"198.51.100.1"}}
	snapshot["api.binance.com"].IPs[0] = "198.51.100.2"
	require.Equal(t, []string{"192.0.2.10"}, cache.Snapshot()["api.binance.com"].IPs)
}
