package inputcache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultConfigChecksAfterFullPrimeOffsetInterval(t *testing.T) {
	cfg := DefaultConfig()
	require.Equal(t, 2233*time.Second, cfg.CheckInterval)
	require.Equal(t, int64(100000), cfg.RebuildKeepRows)
	require.NoError(t, cfg.Validate())
}

func TestCacheConfigRejectsInvalidLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"directory", func(c *Config) { c.Dir = " " }},
		{"capacity", func(c *Config) { c.MaxBytes = 0 }},
		{"interval", func(c *Config) { c.CheckInterval = -time.Second }},
		{"retained rows", func(c *Config) { c.RebuildKeepRows = 0 }},
		{"free space", func(c *Config) { c.MinFreeBytes = -1 }},
		{"timeout", func(c *Config) { c.RebuildTimeout = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.change(&cfg)
			require.Error(t, cfg.Validate())
		})
	}
}

func TestMaintenanceScheduleDoesNotRunImmediatelyOrReenter(t *testing.T) {
	start := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	schedule := NewMaintenanceSchedule(start, 2233*time.Second)
	require.False(t, schedule.TryBegin(start))
	require.False(t, schedule.TryBegin(start.Add(2233*time.Second-time.Nanosecond)))
	require.True(t, schedule.TryBegin(start.Add(2233*time.Second)))
	require.False(t, schedule.TryBegin(start.Add(4466*time.Second)))
	schedule.Finish()
	require.True(t, schedule.TryBegin(start.Add(4466*time.Second)))
	schedule.Finish()
	require.False(t, schedule.TryBegin(start.Add(4466*time.Second)))
}

func TestMaintenanceScheduleAllowsOnlyOneConcurrentCallback(t *testing.T) {
	start := time.Now()
	schedule := NewMaintenanceSchedule(start, time.Second)
	var started atomic.Int32
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			if schedule.TryBegin(start.Add(time.Second)) {
				started.Add(1)
			}
		})
	}
	workers.Wait()
	require.Equal(t, int32(1), started.Load())
	schedule.Finish()
}
