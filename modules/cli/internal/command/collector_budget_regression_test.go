package command

import (
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

func TestStockCollectorOverridesRespectActualExecutionShapes(t *testing.T) {
	fetcher := &setupconfig.SCFFetcherSpace{SpaceID: "stockcn", RealtimeBatchSize: 30, MeasuredSafeGroupSize: 40, MaxInflightRequests: 10, HTTPMaxAttempts: 4, RequestTimeoutMS: 2000, StorageTimeoutMS: 5000}
	require.ErrorContains(t, validateCollectorRuntimeConfig(nil, fetcher, false, 90), "reserves")
	require.NoError(t, validateCollectorRuntimeConfig(nil, fetcher, false, 120))
	require.NoError(t, validateCollectorRuntimeConfig(nil, fetcher, true, 60))
	// A tiny Invoke batch cannot hide a 40-item Timer group's four waves.
	values := map[string]string{"realtime_batch_size": "1", "max_inflight_requests": "1"}
	require.ErrorContains(t, validateCollectorRuntimeConfig(values, fetcher, true, 60), "reserves")
}

func TestStockCollectorNonManifestTimerUsesFortyItemBudget(t *testing.T) {
	credentialFile := setCollectorFleetRuntimeTestEnvironment(t)
	_, err := buildCollectorCreateNodeItem(collectorPublishOptions{SpaceID: "stockcn", EventBusCredentialFile: credentialFile, Config: []string{"realtime_batch_size=1", "max_inflight_requests=1"}}, "pkg")
	require.ErrorContains(t, err, "reserves")
}
