package inputcache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMaintenanceJobDelaysFirstCheckAndRecoversAfterError(t *testing.T) {
	cfg := DefaultConfig()
	now := time.Now()
	calls := 0
	failure := errors.New("rebuild failed")
	job, err := NewMaintenanceJob(cfg, func() time.Time { return now }, func(ctx context.Context) error {
		calls++
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), cfg.RebuildTimeout)
		return failure
	})
	require.NoError(t, err)
	require.NoError(t, job.Handle(context.Background()))
	require.Zero(t, calls)
	now = now.Add(cfg.CheckInterval - time.Nanosecond)
	require.NoError(t, job.Handle(context.Background()))
	require.Zero(t, calls)
	now = now.Add(time.Nanosecond)
	require.ErrorIs(t, job.Handle(context.Background()), failure)
	require.Equal(t, 1, calls)
	require.NoError(t, job.Handle(context.Background()))
	now = now.Add(cfg.CheckInterval)
	require.ErrorIs(t, job.Handle(context.Background()), failure)
	require.Equal(t, 2, calls)
}

func TestDisabledMaintenanceNeverCallsBackend(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	now := time.Now()
	job, err := NewMaintenanceJob(cfg, func() time.Time { return now }, func(context.Context) error {
		t.Fatal("disabled cache ran maintenance")
		return nil
	})
	require.NoError(t, err)
	now = now.Add(2 * cfg.CheckInterval)
	require.NoError(t, job.Handle(context.Background()))
}
