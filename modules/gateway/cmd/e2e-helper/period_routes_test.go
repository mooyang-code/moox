package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
)

func TestCollectorPeriodRoutesProduction(t *testing.T) {
	path, err := filepath.Abs("../../../../config/setup/service-deployments.yaml")
	require.NoError(t, err)
	storage, err := loadCollectorPeriodRoutes(path, "storage-period", "127.0.0.1:23456")
	require.NoError(t, err)
	require.Len(t, storage, 1)
	require.Equal(t, "storage-primary", storage[0].ServiceID)
	require.Equal(t, []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"}, storage[0].AllowedMethods)
	require.Equal(t, []string{"collector"}, storage[0].AllowedCallers)
	require.EqualValues(t, 33554432, storage[0].MaxBodyBytes)
	require.Equal(t, "127.0.0.1:23456", storage[0].Address)
	runtime, err := loadCollectorPeriodRoutes(path, "collector-runtime", "127.0.0.1:23457")
	require.NoError(t, err)
	require.Len(t, runtime, 1)
	require.Equal(t, "collector-market-runtime", runtime[0].ServiceID)
	require.Equal(t, []string{"ClaimTimerBatch"}, runtime[0].AllowedMethods)
	require.Equal(t, []string{"collector"}, runtime[0].AllowedCallers)
	metadata, err := loadCollectorPeriodRoutes(path, "storage-metadata", "127.0.0.1:23458")
	require.NoError(t, err)
	require.Len(t, metadata, 3)
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
	path, err := filepath.Abs("../../../../config/setup/service-deployments.yaml")
	require.NoError(t, err)
	routes, err := loadCollectorPeriodRoutesWithMetadata(path, "storage-period", "127.0.0.1:23456", "127.0.0.1:23458")
	require.NoError(t, err)
	require.Len(t, routes, 4)
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
	_, err = loadCollectorPeriodRoutesWithMetadata(path, "collector-runtime", "127.0.0.1:23456", "127.0.0.1:23458")
	require.Error(t, err)
	_, err = loadCollectorPeriodRoutesWithMetadata(path, "storage-period", "127.0.0.1:23456", "example.com:23458")
	require.Error(t, err)
}

const runtimeSeed = `services:
  - name: collector_market_runtime
    host: 127.0.0.1
    port: 11418
    gateway_path: trpc.moox.collector.MarketFetchRuntime
    gateway_service_id: collector-market-runtime
    gateway_enabled: true
    status: active
    extra_config:
      timeout_ms: 3210
      max_body_bytes: 123456
      gateway_methods: [ClaimTimerBatch, OtherMethod]
      gateway_callers: [collector, audit]
`

func TestCollectorPeriodRoutesPreservePolicyAndFailClosed(t *testing.T) {
	write := func(raw string) string {
		path := filepath.Join(t.TempDir(), "deployment.yaml")
		require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
		return path
	}
	routes, err := loadCollectorPeriodRoutes(write(runtimeSeed), "collector-runtime", "127.0.0.1:23456")
	require.NoError(t, err)
	require.EqualValues(t, 3210, routes[0].TimeoutMS)
	require.EqualValues(t, 123456, routes[0].MaxBodyBytes)
	require.Equal(t, []string{"ClaimTimerBatch", "OtherMethod"}, routes[0].AllowedMethods)
	require.Equal(t, []string{"collector", "audit"}, routes[0].AllowedCallers)
	for name, seed := range map[string]string{
		"missing":            strings.ReplaceAll(runtimeSeed, "ClaimTimerBatch", "Other"),
		"duplicate":          runtimeSeed + strings.TrimPrefix(runtimeSeed, "services:\n"),
		"malformed path":     strings.ReplaceAll(runtimeSeed, "trpc.moox.collector.MarketFetchRuntime", "bad/path"),
		"malformed YAML":     "services: [",
		"disabled":           strings.ReplaceAll(runtimeSeed, "gateway_enabled: true", "gateway_enabled: false"),
		"inactive status":    strings.ReplaceAll(runtimeSeed, "status: active", "status: disabled"),
		"missing status":     strings.ReplaceAll(runtimeSeed, "    status: active\n", ""),
		"missing ID":         strings.ReplaceAll(strings.ReplaceAll(runtimeSeed, "    gateway_service_id: collector-market-runtime\n", ""), "name: collector_market_runtime", "name: collector-market-runtime"),
		"missing caller":     strings.ReplaceAll(runtimeSeed, "[collector, audit]", "[audit]"),
		"invalid timeout":    strings.ReplaceAll(runtimeSeed, "timeout_ms: 3210", "timeout_ms: -1"),
		"invalid body limit": strings.ReplaceAll(runtimeSeed, "max_body_bytes: 123456", "max_body_bytes: -1"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadCollectorPeriodRoutes(write(seed), "collector-runtime", "127.0.0.1:23456")
			require.Error(t, err)
		})
	}
	_, err = loadCollectorPeriodRoutes("relative.yaml", "collector-runtime", "127.0.0.1:23456")
	require.Error(t, err)
	_, err = loadCollectorPeriodRoutes(write(runtimeSeed), "unknown", "127.0.0.1:23456")
	require.Error(t, err)
	_, err = loadCollectorPeriodRoutes(write(runtimeSeed), "collector-runtime", "example.com:23456")
	require.Error(t, err)
	_, err = loadCollectorPeriodRoutes(write(runtimeSeed), "collector-runtime", "127.0.0.1:no-port")
	require.Error(t, err)
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
	seed := filepath.Join(dir, "deployment.yaml")
	ready := filepath.Join(dir, "ready")
	require.NoError(t, os.WriteFile(seed, []byte("services: []"), 0600))
	err := runCollectorPeriodNative(seed, "storage-period", "127.0.0.1:23456", "", "storage-test", "127.0.0.1:0", ready, filepath.Join(dir, "nonces"), "key", "secret")
	require.Error(t, err)
	_, err = os.Stat(ready)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(dir, "nonces"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
