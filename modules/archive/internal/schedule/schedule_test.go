package schedule

import (
	"testing"
	"time"
)

func utc(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

func TestHourQuietWindow(t *testing.T) {
	for _, tc := range []struct {
		at    time.Time
		quiet bool
	}{
		{utc(2026, 10, 17, 12, 57, 59), false},
		{utc(2026, 10, 17, 12, 58, 0), true},
		{utc(2026, 10, 17, 13, 0, 25), true},
		{utc(2026, 10, 17, 13, 3, 59), true},
		{utc(2026, 10, 17, 13, 4, 0), false},
	} {
		if got := InHourQuiet(tc.at); got != tc.quiet {
			t.Fatalf("InHourQuiet(%s) = %v", tc.at, got)
		}
	}
	if end := HourQuietEnd(utc(2026, 10, 17, 12, 59, 0)); !end.Equal(utc(2026, 10, 17, 13, 4, 0)) {
		t.Fatalf("HourQuietEnd = %s", end)
	}
}

func TestBarSpanAndPartitionUnitFollowTheFrequency(t *testing.T) {
	for _, tc := range []struct {
		freq string
		unit string
		span time.Duration
	}{
		{"1m", "day", time.Minute},
		{"1h", "day", time.Hour},
		{"4h", "day", 4 * time.Hour},
		{"1d", "month", 24 * time.Hour},
		{"1w", "month", 7 * 24 * time.Hour},
		{"1mo", "month", 31 * 24 * time.Hour},
	} {
		span, err := BarSpan(tc.freq)
		if err != nil || span != tc.span {
			t.Fatalf("BarSpan(%s) = %s, %v", tc.freq, span, err)
		}
		unit, err := PartitionUnit(tc.freq)
		if err != nil || unit != tc.unit {
			t.Fatalf("PartitionUnit(%s) = %s, %v", tc.freq, unit, err)
		}
	}
	if _, err := BarSpan("7x"); err == nil {
		t.Fatal("未知频率必须报错")
	}
}

func TestPartitionOfDayAndMonth(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// 上海本地日：UTC 16:30 已经是上海次日 00:30。
	key, start, end := PartitionOf("day", sh, time.Date(2026, 10, 16, 16, 30, 0, 0, time.UTC))
	if key != "2026-10-17" || !start.Equal(time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)) || end.Sub(start) != 24*time.Hour {
		t.Fatalf("day partition = %s %s %s", key, start, end)
	}
	utc := time.UTC
	if key, _, _ := PartitionOf("day", utc, time.Date(2026, 10, 16, 23, 59, 0, 0, time.UTC)); key != "2026-10-16" {
		t.Fatalf("crypto 日分区按 UTC 日: %s", key)
	}
	// 日线键两种约定：上海零点（= 前一天 16:00 UTC）和本地日期的 UTC 零点，月初的一根都应落在当月。
	for _, dataTime := range []time.Time{
		time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC), // 上海 10-01 零点
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),  // 美股 10-01 的 UTC 零点
	} {
		key, start, end := PartitionOf("month", utc, dataTime)
		if key != "2026-10" || dataTime.Before(start) || !dataTime.Before(end) {
			t.Fatalf("月初日线 %s 应落在 2026-10 且在分区范围内: %s [%s, %s)", dataTime, key, start, end)
		}
	}
	if key, _, _ := PartitionOf("month", utc, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)); key != "2026-09" {
		t.Fatalf("9 月最后一天应落在 2026-09: %s", key)
	}
}

func TestPartitionClose(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// stockcn 日分区：上海 15:00 收盘。
	key, start, end := PartitionOf("day", sh, time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC))
	closeAt, err := PartitionClose("day", sh, "15:00", "1m", start, end)
	if err != nil || key != "2026-10-17" || !closeAt.Equal(time.Date(2026, 10, 17, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("close = %s err=%v key=%s", closeAt, err, key)
	}
	// crypto 1h 日分区没有本地收盘时刻，取分区结束。
	_, ustart, uend := PartitionOf("day", time.UTC, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if ucl, err := PartitionClose("day", time.UTC, "", "1h", ustart, uend); err != nil || !ucl.Equal(uend) {
		t.Fatalf("crypto 日分区收盘 = %s err=%v", ucl, err)
	}
	// 月分区：分区结束 + 一根 K 线的最大跨度（周线 7 天）。
	mkey, mstart, mend := PartitionOf("month", time.UTC, time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC))
	mclose, _ := PartitionClose("month", time.UTC, "", "1w", mstart, mend)
	// 8 月分区在 data_time 上结束于 08-31 12:00，加一根周线的最大跨度 7 天 = 09-07 12:00。
	if mkey != "2026-08" || !mend.Equal(time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)) || !mclose.Equal(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("月分区收盘 = %s key=%s", mclose, mkey)
	}
	if _, err := PartitionClose("day", sh, "25:99", "1m", start, end); err == nil {
		t.Fatal("非法的本地收盘时刻必须报错")
	}
}
