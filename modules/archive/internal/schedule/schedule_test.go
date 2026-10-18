package schedule

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, spec, tz string) *Plan {
	t.Helper()
	plan, err := Parse(spec, tz)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", spec, tz, err)
	}
	return plan
}

func TestParseRejectsMalformedPlans(t *testing.T) {
	for _, tc := range []struct{ spec, tz, want string }{
		{"", "", "不能为空"},
		{"0 */5 * * *", "", "6 段"},
		{"@hourly", "", "6 段"},
		{"TZ=UTC 0 0 * * * *", "", "6 段"},
		{"0 0 * * * *", "Mars/Base", "时区"},
		{"61 0 * * * *", "", "无法解析"},
		{"0 0 0 30 2 *", "", "5 年"},
	} {
		if _, err := Parse(tc.spec, tc.tz); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("Parse(%q, %q) = %v, want error containing %q", tc.spec, tc.tz, err, tc.want)
		}
	}
}

func TestMinIntervalHandlesUnevenSchedules(t *testing.T) {
	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		spec string
		want time.Duration
	}{
		{"43 4-59/5 * * * *", 5 * time.Minute},
		{"43 34 * * * *", time.Hour},
		{"0 0 17 * * *", 24 * time.Hour},
		// 不均匀：每小时第 0 分与第 3 分，最小间隔 3 分钟。
		{"0 0,3 * * * *", 3 * time.Minute},
		// 每周一次：展开 14 天只有两个时刻。
		{"0 0 8 * * 1", 7 * 24 * time.Hour},
	} {
		plan := mustParse(t, tc.spec, "Asia/Shanghai")
		if got := plan.MinInterval(from); got != tc.want {
			t.Fatalf("MinInterval(%q) = %s, want %s", tc.spec, got, tc.want)
		}
	}
}

func TestCheckIntervalRejectsBelowFloorAndDenseSchedules(t *testing.T) {
	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	if _, err := mustParse(t, "0 */4 * * * *", "").CheckInterval(from, DefaultMinInterval); err == nil {
		t.Fatal("4 分钟间隔必须被拒绝")
	}
	if _, err := mustParse(t, "* * * * * *", "").CheckInterval(from, DefaultMinInterval); err == nil {
		t.Fatal("每秒执行必须被拒绝")
	}
	if gap, err := mustParse(t, "0 */5 * * * *", "").CheckInterval(from, DefaultMinInterval); err != nil || gap != 5*time.Minute {
		t.Fatalf("5 分钟间隔应通过: gap=%s err=%v", gap, err)
	}
	// 恰好在下限之下一秒。
	if _, err := mustParse(t, "0,59 0 * * * *", "").CheckInterval(from, DefaultMinInterval); err == nil {
		t.Fatal("同一分钟内的两个计划时刻必须被拒绝")
	}
}

func TestNextUsesTheConfiguredTimezone(t *testing.T) {
	plan := mustParse(t, "0 0 17 * * *", "Asia/Shanghai")
	next := plan.Next(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("Next = %s, want %s", next, want)
	}
	// 美东跨夏令时：同样写 16:00，UTC 时刻在夏令时结束后移动一小时。
	us := mustParse(t, "0 0 16 * * *", "America/New_York")
	before := us.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) // 11-01 夏令时结束当天
	after := us.Next(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if before.Hour() != 20 && before.Hour() != 21 || after.Hour() != 21 {
		t.Fatalf("夏令时前后的 UTC 时刻不对: %s / %s", before, after)
	}
	if got := plan.NextN(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), 3); len(got) != 3 || got[1].Sub(got[0]) != 24*time.Hour {
		t.Fatalf("NextN = %v", got)
	}
}

func TestReadSpanAndPartitionUnitFollowTheFrequency(t *testing.T) {
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
	got, err := ReadSpan(5*time.Minute, 15*time.Minute, "1m")
	if err != nil || got != 21*time.Minute {
		t.Fatalf("ReadSpan(crypto 1m, 5min) = %s, %v, want 21m", got, err)
	}
	if _, err := BarSpan("7x"); err == nil {
		t.Fatal("未知频率必须报错")
	}
}

func TestDefaultPlanPerSpace(t *testing.T) {
	for _, tc := range []struct{ space, freq, spec string }{
		{"crypto", "1m", "43 34 * * * *"},
		{"crypto", "1h", "43 34 8 * * *"},
		{"stockcn", "1m", "43 4 17 * * *"},
		{"stockhk", "1d", "43 34 17 * * *"},
		{"stockus", "1m", "43 4 6 * * *"},
	} {
		spec, tz := DefaultPlan(tc.space, tc.freq)
		if spec != tc.spec || tz != "Asia/Shanghai" {
			t.Fatalf("DefaultPlan(%s, %s) = %q %q", tc.space, tc.freq, spec, tz)
		}
		if _, err := Parse(spec, tz); err != nil {
			t.Fatalf("默认计划 %q 必须可解析: %v", spec, err)
		}
	}
}

func TestAvoidanceWarnings(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 17, h, m, s, 0, time.UTC) }
	got := AvoidanceWarnings([]time.Time{at(1, 34, 43), at(1, 30, 0), at(2, 59, 10), at(3, 2, 30), at(4, 10, 56)})
	joined := strings.Join(got, "\n")
	for _, want := range []string{"01:30:00", "02:59:10", "03:02:30", "04:10:56"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %s 的避让提示: %v", want, got)
		}
	}
	if strings.Contains(joined, "01:34:43") {
		t.Fatalf("01:34:43 不在避让区间: %v", got)
	}
	if !strings.Contains(joined, "实际读取从 03:04:25 开始") || !strings.Contains(joined, "实际读取从 01:30:25 开始") {
		t.Fatalf("实际开始时刻不对: %v", got)
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

func TestPartitionCloseAndExportEstimate(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// stockcn 日分区：上海 15:00 收盘。
	key, start, end := PartitionOf("day", sh, time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC))
	closeAt, err := PartitionClose("day", sh, "15:00", "1m", start, end)
	if err != nil || key != "2026-10-17" || !closeAt.Equal(time.Date(2026, 10, 17, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("close = %s err=%v key=%s", closeAt, err, key)
	}
	plan := mustParse(t, "43 4 17 * * *", "Asia/Shanghai")
	if got := plan.ExportEstimate(closeAt, 15*time.Minute); !got.Equal(time.Date(2026, 10, 17, 9, 4, 43, 0, time.UTC)) {
		t.Fatalf("stockcn 导出预计 = %s", got)
	}
	// crypto 日分区：UTC 日末，每小时 :34:43 的计划在 00:34:43 导出。
	utcKey, ustart, uend := PartitionOf("day", time.UTC, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	ucl, _ := PartitionClose("day", time.UTC, "", "1m", ustart, uend)
	hourly := mustParse(t, "43 34 * * * *", "Asia/Shanghai")
	if got := hourly.ExportEstimate(ucl, 15*time.Minute); utcKey != "2026-10-16" || !got.Equal(time.Date(2026, 10, 17, 0, 34, 43, 0, time.UTC)) {
		t.Fatalf("crypto 导出预计 = %s key=%s", got, utcKey)
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
