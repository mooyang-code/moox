package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthReportsStorageWriteLatch(t *testing.T) {
	health := NewHealth(time.Minute)
	for range 3 {
		health.RecordStorageWrite(false)
	}
	status := health.Check(time.Now())
	require.False(t, status.Healthy)
	require.Contains(t, status.Reasons, "storage write failure latch is open")
	health.RecordStorageWrite(true)
	require.True(t, health.Check(time.Now()).Healthy)
}

func TestHealthReportsStuckRunner(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	health := NewHealth(time.Minute)
	health.StartRunner("set-a", now.Add(-2*time.Minute-time.Second))
	status := health.Check(now)
	require.False(t, status.Healthy)
	require.Contains(t, status.Reasons, "factor task runner set-a is stuck")
	health.EndRunner("set-a")
	require.True(t, health.Check(now).Healthy)
}
