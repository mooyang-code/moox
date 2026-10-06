package marketfetch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceRunnerObservesFailedAndSuccessfulPasses(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	runner := NewMaintenanceRunner(time.Hour, 0, time.Minute, func(context.Context) error { return errors.New("test failure") })
	runner.Metrics = m
	runner.runPass(context.Background())
	require.Equal(t, 1.0, testutil.ToFloat64(m.maintenancePasses.WithLabelValues("error")))
	require.Zero(t, testutil.ToFloat64(m.maintenanceLastSuccess.WithLabelValues()))
	runner.pass = func(context.Context) error { return nil }
	runner.runPass(context.Background())
	require.Equal(t, 1.0, testutil.ToFloat64(m.maintenancePasses.WithLabelValues("success")))
	require.Positive(t, testutil.ToFloat64(m.maintenanceLastSuccess.WithLabelValues()))
}

func TestMaintenanceRunnerStartsAtOffsetInsideInterval(t *testing.T) {
	runner := NewMaintenanceRunner(time.Minute, 35*time.Second, 20*time.Second, func(context.Context) error { return nil })
	at := func(minute, second int) time.Time { return time.Date(2026, 10, 6, 15, minute, second, 0, time.UTC) }
	require.Equal(t, at(0, 35), runner.nextRun(at(0, 0)), "the minute-boundary market tick must not share its window with cleanup")
	require.Equal(t, at(0, 35), runner.nextRun(at(0, 34)))
	require.Equal(t, at(1, 35), runner.nextRun(at(0, 35)))
	require.Equal(t, at(1, 35), runner.nextRun(at(0, 59)))
}

func TestMaintenanceRunnerRunsPassesSerially(t *testing.T) {
	var active, maxActive, passes atomic.Int32
	done := make(chan struct{})
	runner := NewMaintenanceRunner(40*time.Millisecond, 10*time.Millisecond, 25*time.Millisecond, func(ctx context.Context) error {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		<-ctx.Done()
		active.Add(-1)
		if passes.Add(1) == 3 {
			close(done)
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, runner.Start(ctx))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance passes did not run on schedule")
	}
	require.EqualValues(t, 1, maxActive.Load(), "only one maintenance pass may run at a time")
}

func TestMaintenanceRunnerValidatesScheduleAndSingleStart(t *testing.T) {
	pass := func(context.Context) error { return nil }
	for _, test := range []struct {
		interval, offset, timeout time.Duration
	}{
		{interval: 0, timeout: time.Second},
		{interval: time.Minute, timeout: 0},
		{interval: time.Second, timeout: time.Minute},
		{interval: time.Minute, offset: -time.Second, timeout: time.Second},
		{interval: time.Minute, offset: time.Minute, timeout: time.Second},
		{interval: time.Minute, offset: 35 * time.Second, timeout: 30 * time.Second},
	} {
		require.Error(t, NewMaintenanceRunner(test.interval, test.offset, test.timeout, pass).Start(context.Background()))
	}
	runner := NewMaintenanceRunner(time.Hour, 0, time.Minute, pass)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, runner.Start(ctx))
	require.Error(t, runner.Start(ctx))
}
