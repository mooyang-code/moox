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
	runner := NewMaintenanceRunner(time.Hour, time.Minute, func(context.Context) error { return errors.New("test failure") })
	runner.Metrics = m
	runner.runPass(context.Background())
	require.Equal(t, 1.0, testutil.ToFloat64(m.maintenancePasses.WithLabelValues("error")))
	require.Zero(t, testutil.ToFloat64(m.maintenanceLastSuccess.WithLabelValues()))
	runner.pass = func(context.Context) error { return nil }
	runner.runPass(context.Background())
	require.Equal(t, 1.0, testutil.ToFloat64(m.maintenancePasses.WithLabelValues("success")))
	require.Positive(t, testutil.ToFloat64(m.maintenanceLastSuccess.WithLabelValues()))
}

func TestMaintenanceRunnerCoalescesWakeupsAndSerializesPasses(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	var passes atomic.Int32
	runner := NewMaintenanceRunner(200*time.Millisecond, 150*time.Millisecond, func(ctx context.Context) error {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		passes.Add(1)
		started <- struct{}{}
		select {
		case <-ctx.Done():
		case <-release:
		}
		active.Add(-1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, runner.Start(ctx))
	for range 8 {
		runner.Wake()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("maintenance pass did not start")
	}
	for range 8 {
		runner.Wake()
	}
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, passes.Load(), "wakeups during a pass must coalesce")
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("coalesced maintenance wake did not run after the active pass")
	}
	require.EqualValues(t, 1, maxActive.Load(), "only one maintenance pass may run at a time")
}

func TestMaintenanceRunnerValidatesScheduleAndSingleStart(t *testing.T) {
	pass := func(context.Context) error { return nil }
	for _, test := range []struct {
		interval time.Duration
		timeout  time.Duration
	}{
		{interval: 0, timeout: time.Second},
		{interval: time.Minute, timeout: 0},
		{interval: time.Second, timeout: time.Minute},
	} {
		require.Error(t, NewMaintenanceRunner(test.interval, test.timeout, pass).Start(context.Background()))
	}
	runner := NewMaintenanceRunner(time.Hour, time.Minute, pass)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, runner.Start(ctx))
	require.Error(t, runner.Start(ctx))
}
