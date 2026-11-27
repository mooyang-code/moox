package engine

import (
	"sync"
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
	handle := health.StartRunner("set-a", now.Add(-2*time.Minute-time.Second))
	require.NotZero(t, handle)
	status := health.Check(now)
	require.False(t, status.Healthy)
	require.Contains(t, status.Reasons, "factor task runner set-a is stuck")
	health.EndRunner(handle)
	require.True(t, health.Check(now).Healthy)
}

// 同一任务同时运行实时周期与补算分块时各自持有唯一句柄：后启动的不覆盖先启动的起始时间，先结束的不清除仍在卡死的运行。
func TestHealthTracksConcurrentRunsForSameTaskIndependently(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	health := NewHealth(time.Minute)

	recalcHandle := health.StartRunner("set-a", now.Add(-3*time.Minute))
	liveHandle := health.StartRunner("set-a", now.Add(-10*time.Second))
	require.NotEqual(t, recalcHandle, liveHandle)

	// 实时周期刚启动，但补算分块已超时：必须报告卡死。
	status := health.Check(now)
	require.False(t, status.Healthy)
	require.Equal(t, []string{"factor task runner set-a is stuck"}, status.Reasons)

	// 实时周期先结束：不能清掉仍在卡死的补算分块。
	health.EndRunner(liveHandle)
	status = health.Check(now)
	require.False(t, status.Healthy)
	require.Equal(t, []string{"factor task runner set-a is stuck"}, status.Reasons)

	// 同一任务有多个运行都卡死时原因去重。
	stuck2 := health.StartRunner("set-a", now.Add(-5*time.Minute))
	status = health.Check(now)
	require.False(t, status.Healthy)
	require.Equal(t, []string{"factor task runner set-a is stuck"}, status.Reasons)

	// 两个卡死运行都结束后恢复健康；重复 EndRunner 与零值句柄均安全。
	health.EndRunner(recalcHandle)
	require.False(t, health.Check(now).Healthy)
	health.EndRunner(stuck2)
	health.EndRunner(stuck2)
	health.EndRunner(0)
	require.True(t, health.Check(now).Healthy)
}

func TestHealthConcurrentStartEndCheck(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	health := NewHealth(time.Minute)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				h := health.StartRunner("set-a", now)
				_ = health.Check(now)
				health.EndRunner(h)
			}
		}()
	}
	wg.Wait()
	require.True(t, health.Check(now).Healthy)
}
