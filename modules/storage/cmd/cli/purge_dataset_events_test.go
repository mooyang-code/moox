package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPurgeDatasetEventsRequiresConfirmation(t *testing.T) {
	err := runPurgeDatasetEvents(context.Background(), []string{"--space", "moox_system", "--dataset", "moox_service_metrics"}, &bytes.Buffer{})
	require.ErrorContains(t, err, "--yes")
}

func TestPurgeDatasetEventsDryRunPrintsExactSubjects(t *testing.T) {
	var stdout bytes.Buffer
	err := runPurgeDatasetEvents(context.Background(), []string{"--space", "moox_system", "--dataset", "moox_service_metrics", "--dry-run"}, &stdout)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), `"status":"dry_run"`)
	require.Contains(t, stdout.String(), "moox.storage.dataset.rows.upserted.v2.")
	require.Contains(t, stdout.String(), "moox.storage.dataset.period.collected.v1.")
	require.Contains(t, stdout.String(), "moox.storage.dataset.sync_point.v1.")
}

func TestDatasetEventSubjectsAreExactAndDatasetScoped(t *testing.T) {
	subjects, err := datasetEventSubjects("moox_system", "moox_service_metrics")
	require.NoError(t, err)
	require.Equal(t, []string{
		"moox.storage.dataset.rows.upserted.v2.nvxw66c7on4xg5dfnu.nvxw66c7onsxe5tjmnsv63lforzgsy3t",
		"moox.storage.dataset.period.collected.v1.nvxw66c7on4xg5dfnu.nvxw66c7onsxe5tjmnsv63lforzgsy3t",
		"moox.storage.dataset.factor_period.computed.v1.nvxw66c7on4xg5dfnu.nvxw66c7onsxe5tjmnsv63lforzgsy3t",
		"moox.storage.dataset.sync_point.v1.nvxw66c7on4xg5dfnu.nvxw66c7onsxe5tjmnsv63lforzgsy3t",
	}, subjects)
	other, err := datasetEventSubjects("moox_system", "other")
	require.NoError(t, err)
	require.NotEqual(t, subjects, other)
}

func TestValidatePurgeEventBusURLsRejectsPlaintextRemoteAdminCredential(t *testing.T) {
	require.Error(t, validatePurgeEventBusURLs([]string{"nats://203.0.113.10:4222"}, ""))
	require.Error(t, validatePurgeEventBusURLs([]string{"tls://203.0.113.10:4222"}, ""))
	require.NoError(t, validatePurgeEventBusURLs([]string{"nats://127.0.0.1:4222"}, ""))
	require.NoError(t, validatePurgeEventBusURLs([]string{"tls://203.0.113.10:4222"}, "ca.pem"))
}

func TestPurgeDatasetEventsCallsPurgeAfterConfirmation(t *testing.T) {
	original := purgeDatasetEventSubjects
	t.Cleanup(func() { purgeDatasetEventSubjects = original })
	called := false
	purgeDatasetEventSubjects = func(_ context.Context, opts purgeDatasetEventsOptions, subjects []string) error {
		called = true
		require.Equal(t, "MOOX_STORAGE", opts.stream)
		require.Len(t, subjects, 4)
		return nil
	}
	var stdout bytes.Buffer
	err := runPurgeDatasetEvents(context.Background(), []string{"--space", "moox_system", "--dataset", "moox_service_metrics", "--yes"}, &stdout)
	require.NoError(t, err)
	require.True(t, called)
}
