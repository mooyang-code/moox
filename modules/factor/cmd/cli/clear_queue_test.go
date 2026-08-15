package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseClearQueueDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"clear-queue", "--yes"})
	require.NoError(t, err)
	require.Equal(t, "MOOX_STORAGE", cfg.Stream)
	require.Equal(t, "factor_view_ready_v1", cfg.Consumer)
	require.Equal(t, 2*time.Minute, cfg.Timeout)
	require.True(t, cfg.Restart)
	require.True(t, cfg.Yes)
}

func TestClearQueueDryRunDoesNotTouchConsumerOrLifecycle(t *testing.T) {
	oldDelete := deleteFactorQueueConsumer
	oldLifecycle := runFactorLifecycle
	t.Cleanup(func() {
		deleteFactorQueueConsumer = oldDelete
		runFactorLifecycle = oldLifecycle
	})
	deleteFactorQueueConsumer = func(context.Context, factorQueueConsumerOptions) (clearQueueSummary, error) {
		t.Fatal("dry-run must not connect to NATS")
		return clearQueueSummary{}, nil
	}
	runFactorLifecycle = func(context.Context, string, string) error {
		t.Fatal("dry-run must not restart Factor")
		return nil
	}

	var out bytes.Buffer
	err := runClearQueue(context.Background(), cliConfig{
		Stream: "MOOX_STORAGE", Consumer: "factor_view_ready_v1", Timeout: time.Minute, DryRun: true,
	}, &out)
	require.NoError(t, err)
	var result struct {
		Status  string            `json:"status"`
		Summary clearQueueSummary `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "dry_run", result.Status)
	require.True(t, result.Summary.DryRun)
}

func TestClearQueueStopsDeletesAndStartsFactor(t *testing.T) {
	oldDelete := deleteFactorQueueConsumer
	oldLifecycle := runFactorLifecycle
	t.Cleanup(func() {
		deleteFactorQueueConsumer = oldDelete
		runFactorLifecycle = oldLifecycle
	})
	var calls []string
	runFactorLifecycle = func(_ context.Context, _ string, action string) error {
		calls = append(calls, action)
		return nil
	}
	deleteFactorQueueConsumer = func(_ context.Context, opts factorQueueConsumerOptions) (clearQueueSummary, error) {
		require.Equal(t, "MOOX_STORAGE", opts.Stream)
		require.Equal(t, "factor_view_ready_v1", opts.Consumer)
		calls = append(calls, "delete")
		return clearQueueSummary{Stream: opts.Stream, Consumer: opts.Consumer, Deleted: true, Pending: 12, AckPending: 1}, nil
	}

	var out bytes.Buffer
	err := runClearQueue(context.Background(), cliConfig{
		Stream: "MOOX_STORAGE", Consumer: "factor_view_ready_v1", Timeout: time.Minute,
		PackageRoot: t.TempDir(), Yes: true, Restart: true,
	}, &out)
	require.NoError(t, err)
	require.Equal(t, []string{"stop", "delete", "start"}, calls)
	var result struct {
		Status  string            `json:"status"`
		Summary clearQueueSummary `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "ok", result.Status)
	require.Equal(t, uint64(12), result.Summary.Pending)
	require.Equal(t, 1, result.Summary.AckPending)
	require.True(t, result.Summary.Restarted)
}
