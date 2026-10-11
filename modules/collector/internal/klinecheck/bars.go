package klinecheck

import (
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// alignUp 返回不早于 t 的第一个 K 线开始时间；月线对齐到 UTC 月初。
func alignUp(frequency marketdata.Frequency, t time.Time) time.Time {
	t = t.UTC()
	if frequency.Duration() > 0 {
		seconds := int64(frequency.Duration() / time.Second)
		return time.Unix((t.Unix()+seconds-1)/seconds*seconds, 0).UTC()
	}
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	if start.Before(t) {
		start = start.AddDate(0, 1, 0)
	}
	return start
}

// alignDown 返回不晚于 t 的最近一个 K 线开始时间。
func alignDown(frequency marketdata.Frequency, t time.Time) time.Time {
	t = t.UTC()
	if frequency.Duration() > 0 {
		return t.Truncate(frequency.Duration())
	}
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// barCount 返回 [from, to) 内的 K 线根数；月线按日历逐月计数。
func barCount(frequency marketdata.Frequency, from, to time.Time) int {
	if !from.Before(to) {
		return 0
	}
	if frequency.Duration() > 0 {
		return int(to.Sub(from) / frequency.Duration())
	}
	count := 0
	for t := from; t.Before(to); t = frequency.BarEnd(t) {
		count++
	}
	return count
}

// advanceBars 从 from 起前进 count 根 K 线后的时间。
func advanceBars(frequency marketdata.Frequency, from time.Time, count int) time.Time {
	t := from
	for i := 0; i < count; i++ {
		t = frequency.BarEnd(t)
	}
	return t
}
