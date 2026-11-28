package hostmetrics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/require"
)

func TestForecastDisksThresholdsAndMultipleMounts(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	const gib = uint64(1 << 30)
	points := make([]HistoryPoint, 0, 4)
	for day := 3; day >= 0; day-- {
		points = append(points, HistoryPoint{ObservedAt: now.Add(-time.Duration(day) * 24 * time.Hour), Snapshot: &hostmetricpb.HostSnapshot{Filesystems: []*hostmetricpb.FilesystemMetric{
			{Mountpoint: "/", TotalBytes: 100 * gib, UsedBytes: (80 + uint64(3-day)*2) * gib},
			{Mountpoint: "/data", TotalBytes: 200 * gib, UsedBytes: (100 - uint64(3-day)) * gib},
		}}})
	}
	got := ForecastDisks(points, now)
	require.Len(t, got, 2)
	require.Equal(t, "/", got[0].Mountpoint)
	require.Equal(t, "FAIL", got[0].Status)
	require.InDelta(t, 2*float64(gib), got[0].GrowthBytesPerDay, 1)
	require.Equal(t, "PASS", got[1].Status)
	require.Contains(t, got[1].Summary, "not currently growing")
}

func TestForecastDisksInsufficientSamples(t *testing.T) {
	now := time.Now().UTC()
	points := []HistoryPoint{{ObservedAt: now.Add(-time.Hour), Snapshot: &hostmetricpb.HostSnapshot{Filesystems: []*hostmetricpb.FilesystemMetric{{Mountpoint: "/", TotalBytes: 100, UsedBytes: 50}}}}}
	got := ForecastDisks(points, now)
	require.Equal(t, "UNKNOWN", got[0].Status)
	require.Equal(t, 0, got[0].ValidIntervals, fmt.Sprint(got[0]))
}

func TestForecastDisksUsesDailySamplesInsteadOfBurstRate(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	const gib = uint64(1 << 30)
	points := make([]HistoryPoint, 0, 32)
	for day := 0; day < 7; day++ {
		for burst := 0; burst < 4; burst++ {
			at := now.Add(-time.Duration(6-day)*24*time.Hour + time.Duration(burst)*10*time.Minute)
			used := uint64(50+day) * gib
			points = append(points, HistoryPoint{ObservedAt: at, Snapshot: &hostmetricpb.HostSnapshot{Filesystems: []*hostmetricpb.FilesystemMetric{{Mountpoint: "/", TotalBytes: 100 * gib, UsedBytes: used}}}})
		}
	}
	got := ForecastDisks(points, now)
	require.Len(t, got, 1)
	require.Greater(t, got[0].GrowthBytesPerDay, 0.5*float64(gib))
	require.GreaterOrEqual(t, got[0].ValidIntervals, 3)
}

func TestDailyLatestHistoryMatchesFullHistoryForecast(t *testing.T) {
	samples := NewSampleStore(openRegistryTestDB(t))
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	const gib = uint64(1 << 30)
	ctx := context.Background()

	// 每天写多条快照，另外写入窗口外的一条旧样本。
	require.NoError(t, samples.Write(ctx, "storage", now.Add(-8*24*time.Hour), &hostmetricpb.HostSnapshot{
		Filesystems: []*hostmetricpb.FilesystemMetric{{Mountpoint: "/", TotalBytes: 100 * gib, UsedBytes: 10 * gib}},
	}))
	for day := 5; day >= 0; day-- {
		for step := 0; step < 3; step++ {
			at := now.Add(-time.Duration(day)*24*time.Hour - time.Duration(2-step)*time.Hour)
			used := uint64(60+(5-day)*3)*gib + uint64(step)*100*(1<<20)
			require.NoError(t, samples.Write(ctx, "storage", at, &hostmetricpb.HostSnapshot{
				Filesystems: []*hostmetricpb.FilesystemMetric{{Mountpoint: "/", TotalBytes: 100 * gib, UsedBytes: used}},
			}))
		}
	}

	start := now.Add(-DiskForecastWindow)
	full, err := samples.History(ctx, "storage", start, now, ForecastHistoryLimit)
	require.NoError(t, err)
	daily, err := samples.DailyLatestHistory(ctx, "storage", start, now)
	require.NoError(t, err)
	require.Len(t, daily, 6)
	require.Equal(t, ForecastDisks(full, now), ForecastDisks(daily, now))
}
