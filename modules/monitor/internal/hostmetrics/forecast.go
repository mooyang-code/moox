package hostmetrics

import (
	"math"
	"sort"
	"time"
)

const (
	// DiskForecastWindow 是磁盘写满预测回看的历史窗口（最近 7 天，与主机快照保留期一致）。
	DiskForecastWindow = 7 * 24 * time.Hour
	// DiskForecastAlertDays 是磁盘写满预测主机检查的告警阈值（天）：按日增长率中位数估算，距耗尽安全余量（10% 且不少于 5 GiB）不超过 7 天即告警。
	DiskForecastAlertDays = 7
	// DiskForecastRecoveryDays 是磁盘写满预测主机检查的恢复线（天）：告警触发后需回到超过 10 天（或磁盘停止增长）才恢复，防止在阈值附近抖动。
	DiskForecastRecoveryDays = 10

	forecastWindow       = DiskForecastWindow
	minimumForecastGaps  = 3
	minimumDiskReserve   = uint64(5 << 30)
	warningRemainingDays = 14
	failingRemainingDays = DiskForecastAlertDays
	maxDailyInterval     = 36 * time.Hour
)

type DiskForecast struct {
	Mountpoint        string
	Status            string
	GrowthBytesPerDay float64
	RemainingDays     float64
	ValidIntervals    int
	Summary           string
}

// ForecastDisks derives a conservative per-mount forecast from existing host
// samples. No value is invented when history is too sparse or usage is flat.
func ForecastDisks(points []HistoryPoint, now time.Time) []DiskForecast {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	type sample struct {
		at          time.Time
		total, used uint64
	}
	type dailySample struct {
		day         string
		at          time.Time
		total, used uint64
	}
	byMount := map[string][]sample{}
	for _, point := range points {
		at := point.ObservedAt
		if at.IsZero() || at.Before(now.Add(-forecastWindow)) || at.After(now) || point.Snapshot == nil {
			continue
		}
		for _, fs := range point.Snapshot.GetFilesystems() {
			if fs.GetMountpoint() == "" || fs.GetTotalBytes() == 0 || fs.GetUsedBytes() > fs.GetTotalBytes() {
				continue
			}
			byMount[fs.GetMountpoint()] = append(byMount[fs.GetMountpoint()], sample{at: at, total: fs.GetTotalBytes(), used: fs.GetUsedBytes()})
		}
	}
	mounts := make([]string, 0, len(byMount))
	for mount := range byMount {
		mounts = append(mounts, mount)
	}
	sort.Strings(mounts)
	out := make([]DiskForecast, 0, len(mounts))
	for _, mount := range mounts {
		samples := byMount[mount]
		sort.Slice(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
		daily := map[string]dailySample{}
		for _, point := range samples {
			day := point.at.UTC().Format("2006-01-02")
			if previous, ok := daily[day]; !ok || point.at.After(previous.at) {
				daily[day] = dailySample{day: day, at: point.at, total: point.total, used: point.used}
			}
		}
		days := make([]dailySample, 0, len(daily))
		for _, point := range daily {
			days = append(days, point)
		}
		sort.Slice(days, func(i, j int) bool { return days[i].at.Before(days[j].at) })
		rates := make([]float64, 0, len(days)-1)
		for i := 1; i < len(days); i++ {
			elapsedDuration := days[i].at.Sub(days[i-1].at)
			if elapsedDuration > maxDailyInterval {
				continue
			}
			elapsed := elapsedDuration.Hours() / 24
			if elapsed <= 0 {
				continue
			}
			delta := float64(days[i].used) - float64(days[i-1].used)
			rates = append(rates, delta/elapsed)
		}
		forecast := DiskForecast{Mountpoint: mount, Status: "UNKNOWN", ValidIntervals: len(rates), Summary: "insufficient disk history"}
		if len(rates) >= minimumForecastGaps {
			latest := days[len(days)-1]
			if latest.total <= minimumDiskReserve {
				forecast.Status = "SKIPPED"
				forecast.Summary = "partition size does not exceed minimum reserve; forecast skipped"
				out = append(out, forecast)
				continue
			}
			sort.Float64s(rates)
			growth := median(rates)
			forecast.GrowthBytesPerDay = growth
			if growth <= 0 {
				forecast.Status = "PASS"
				forecast.Summary = "disk usage is not currently growing"
			} else {
				reserve := latest.total / 10
				if reserve < minimumDiskReserve {
					reserve = minimumDiskReserve
				}
				available := float64(0)
				if latest.total > latest.used+reserve {
					available = float64(latest.total - latest.used - reserve)
				}
				forecast.RemainingDays = available / growth
				forecast.Status = "PASS"
				forecast.Summary = "disk capacity is above the warning horizon"
				if forecast.RemainingDays <= failingRemainingDays {
					forecast.Status, forecast.Summary = "FAIL", "disk capacity is within the failure horizon"
				} else if forecast.RemainingDays <= warningRemainingDays {
					forecast.Status, forecast.Summary = "WARN", "disk capacity is within the warning horizon"
				}
			}
		}
		out = append(out, forecast)
	}
	return out
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}
