package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
)

func TestCollectorPeriodRoutesProduction(t *testing.T) {
	storage, err := loadCollectorPeriodRoutes("storage-period", "127.0.0.1:23456")
	require.NoError(t, err)
	require.Len(t, storage, 1)
	require.Equal(t, "storage-primary", storage[0].ServiceID)
	require.Equal(t, []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"}, storage[0].AllowedMethods)
	require.Equal(t, []string{"collector"}, storage[0].AllowedCallers)
	require.EqualValues(t, 33554432, storage[0].MaxBodyBytes)
	require.Equal(t, "127.0.0.1:23456", storage[0].Address)
	runtime, err := loadCollectorPeriodRoutes("collector-runtime", "127.0.0.1:23457")
	require.NoError(t, err)
	require.Len(t, runtime, 1)
	require.Equal(t, "collector", runtime[0].ServiceID)
	require.Equal(t, []string{"ClaimTimerBatch"}, runtime[0].AllowedMethods)
	require.Equal(t, []string{"collector"}, runtime[0].AllowedCallers)
	metadata, err := loadCollectorPeriodRoutes("storage-metadata", "127.0.0.1:23458")
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	methods := map[string]int{}
	for _, route := range metadata {
		require.Equal(t, "storage-primary", route.ServiceID)
		require.Equal(t, "trpc.moox.storage.Metadata", route.ServicePath)
		require.True(t, route.AllowsCaller("collector"))
		require.Equal(t, "127.0.0.1:23458", route.Address)
		for _, method := range route.AllowedMethods {
			methods[method]++
		}
		require.NotContains(t, route.AllowedMethods, "UpsertTag")
	}
	require.Equal(t, 1, methods["ApplyTagSnapshot"])
	require.Equal(t, 1, methods["GetTag"])
	require.Equal(t, 1, methods["ListSubjects"])
	require.Equal(t, 1, methods["ResolveSubjects"])
}

func TestCollectorPeriodRoutesCombineStorageAndMetadata(t *testing.T) {
	routes, err := loadCollectorPeriodRoutesWithMetadata("storage-period", "127.0.0.1:23456", "127.0.0.1:23458")
	require.NoError(t, err)
	require.Len(t, routes, 2)
	methods := map[string]int{}
	for _, route := range routes {
		for _, method := range route.AllowedMethods {
			methods[method]++
		}
		if route.ServicePath == "trpc.moox.storage.Metadata" {
			require.Equal(t, "127.0.0.1:23458", route.Address)
		} else {
			require.Equal(t, "trpc.moox.storage.PrimaryStore", route.ServicePath)
			require.Equal(t, "127.0.0.1:23456", route.Address)
		}
	}
	for _, method := range []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus", "ApplyTagSnapshot", "GetTag", "ListSubjects", "ResolveSubjects"} {
		require.Equal(t, 1, methods[method], method)
	}
	_, err = loadCollectorPeriodRoutesWithMetadata("collector-runtime", "127.0.0.1:23456", "127.0.0.1:23458")
	require.Error(t, err)
	_, err = loadCollectorPeriodRoutesWithMetadata("storage-period", "127.0.0.1:23456", "example.com:23458")
	require.Error(t, err)
}

func TestCollectorPeriodRoutesRejectUnknownScopesAndNonLoopbackTargets(t *testing.T) {
	for _, target := range []string{"example.com:23456", "127.0.0.1:no-port", "127.0.0.1:0", "127.0.0.1:65536", "0.0.0.0:23456"} {
		_, err := loadCollectorPeriodRoutes("collector-runtime", target)
		require.Error(t, err)
	}
	_, err := loadCollectorPeriodRoutes("unknown", "127.0.0.1:23456")
	require.Error(t, err)
	routes, err := loadCollectorPeriodRoutes("collector-runtime", "[::1]:23456")
	require.NoError(t, err)
	require.EqualValues(t, 5000, routes[0].TimeoutMS)
	require.EqualValues(t, 4194304, routes[0].MaxBodyBytes)
	require.Equal(t, []string{"ClaimTimerBatch"}, routes[0].AllowedMethods)
}

func TestNativeRoutesRejectInvalidIdentityBeforeStartup(t *testing.T) {
	for _, config := range []struct{ name, node, key, secret string }{
		{"empty node", "", "key", "secret"},
		{"invalid node", " node", "key", "secret"},
		{"empty key", "node", "", "secret"},
		{"invalid key", "node", " key", "secret"},
		{"empty secret", "node", "key", ""},
	} {
		t.Run(config.name, func(t *testing.T) {
			dir := t.TempDir()
			ready, nonces := filepath.Join(dir, "ready"), filepath.Join(dir, "nonces")
			routes := []gatewayroute.Route{{ServiceID: "collector-market-runtime", Address: "127.0.0.1:23456", ServicePath: "trpc.moox.collector.MarketFetchRuntime", AllowedMethods: []string{"ClaimTimerBatch"}, AllowedCallers: []string{"collector"}}}
			err := runNativeRoutes(config.node, routes, "collector", "invalid-listener", ready, nonces, config.key, config.secret)
			require.Error(t, err)
			require.Contains(t, err.Error(), "native gateway identity")
			_, err = os.Stat(ready)
			require.ErrorIs(t, err, os.ErrNotExist)
			_, err = os.Stat(nonces)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestCollectorPeriodNativeRejectsRoutesBeforeReady(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	err := runCollectorPeriodNative("unknown", "127.0.0.1:23456", "", "storage-test", "127.0.0.1:0", ready, filepath.Join(dir, "nonces"), "key", "secret")
	require.Error(t, err)
	_, err = os.Stat(ready)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(dir, "nonces"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
