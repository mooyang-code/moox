package periodclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cst 返回北京时间的某个时刻（UTC 表示）。
func cst(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", value, time.FixedZone("CST", 8*3600))
	require.NoError(t, err)
	return at.UTC()
}

func stockAxis(t *testing.T) Axis {
	t.Helper()
	axis, err := AxisFor("stockcn", "5m")
	require.NoError(t, err)
	require.False(t, axis.Identity())
	return axis
}

func TestForSpaceStockCNUsesSession(t *testing.T) {
	clock, err := ForSpace("stockcn")
	require.NoError(t, err)
	_, ok := clock.(*Session)
	require.True(t, ok)
	again, err := ForSpace("stockcn")
	require.NoError(t, err)
	require.Same(t, clock, again, "按空间缓存同一个时钟")

	duration, err := clock.Duration("5m")
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, duration)
	_, err = clock.Duration("4h")
	require.ErrorIs(t, err, ErrUnsupportedFrequency, "4h 不能整除交易时段")
}

// 相邻两根交易 K 线在轴上相差一个周期：跨午休、隔夜、周末、国庆长假都一样。
func TestSessionAxisIsContinuousAcrossBreaks(t *testing.T) {
	axis := stockAxis(t)
	for _, pair := range [][2]string{
		{"2026-10-08 11:25", "2026-10-08 13:00"}, // 午休
		{"2026-10-08 14:55", "2026-10-09 09:30"}, // 隔夜
		{"2026-10-09 14:55", "2026-10-12 09:30"}, // 周末
		{"2026-09-30 14:55", "2026-10-08 09:30"}, // 国庆
		{"2026-10-08 09:30", "2026-10-08 09:35"}, // 盘中
	} {
		before, after := cst(t, pair[0]), cst(t, pair[1])
		a, err := axis.ToAxis(before)
		require.NoError(t, err, pair[0])
		b, err := axis.ToAxis(after)
		require.NoError(t, err, pair[1])
		require.Equal(t, 5*time.Minute, b.Sub(a), "%s → %s", pair[0], pair[1])

		back, err := axis.FromAxis(a)
		require.NoError(t, err)
		require.Equal(t, before, back)
		back, err = axis.FromAxis(b)
		require.NoError(t, err)
		require.Equal(t, after, back)
	}
}

func TestSessionToAxisRejectsNonTradingTimes(t *testing.T) {
	axis := stockAxis(t)
	for _, value := range []string{
		"2026-10-08 12:00", // 午休
		"2026-10-08 11:30", // 上午收盘（不是桶起点）
		"2026-10-08 15:00", // 收盘
		"2026-10-10 10:00", // 周六
		"2026-10-01 10:00", // 国庆
		"2026-10-08 09:32", // 没对齐
	} {
		_, err := axis.ToAxis(cst(t, value))
		require.Error(t, err, value)
	}
}

// AxisEnd 是“收盘不晚于 t 的最新一根 + 周期”：休市期间停在最后一根之后，桶起点上与 ToAxis 一致。
func TestSessionAxisEnd(t *testing.T) {
	axis := stockAxis(t)
	toAxis := func(value string) time.Time {
		x, err := axis.ToAxis(cst(t, value))
		require.NoError(t, err, value)
		return x
	}
	step := 5 * time.Minute
	for _, c := range []struct {
		now  string
		want time.Time
	}{
		{"2026-10-08 11:27", toAxis("2026-10-08 11:25")},           // 11:25 那根还没收盘
		{"2026-10-08 11:30", toAxis("2026-10-08 11:25").Add(step)}, // 刚收盘
		{"2026-10-08 12:10", toAxis("2026-10-08 13:00")},           // 午休：= 下午第一根
		{"2026-10-08 13:00", toAxis("2026-10-08 13:00")},           // 桶起点：与 ToAxis 一致
		{"2026-10-08 20:00", toAxis("2026-10-08 14:55").Add(step)}, // 收盘后
		{"2026-10-11 10:00", toAxis("2026-10-12 09:30")},           // 周日
		{"2026-10-05 10:00", toAxis("2026-10-08 09:30")},           // 国庆中
		{"2026-09-30 15:00", toAxis("2026-09-30 14:55").Add(step)}, // 节前收盘
		{"2026-10-08 09:35", toAxis("2026-10-08 09:30").Add(step)}, // 节后第一根刚收盘
		{"2026-10-08 09:34", toAxis("2026-09-30 14:55").Add(step)}, // 节后第一根还没收盘
		{"2026-10-08 09:30", toAxis("2026-10-08 09:30")},           // 节后开盘
		{"2026-10-08 13:05", toAxis("2026-10-08 13:00").Add(step)}, // 下午第一根刚收盘
		{"2026-10-08 11:25", toAxis("2026-10-08 11:25")},           // 上午最后一根开盘
		{"2026-10-08 09:00", toAxis("2026-10-08 09:30")},           // 开盘前
		{"2026-10-09 09:29", toAxis("2026-10-09 09:30")},           // 次日开盘前
		{"2026-10-08 14:59", toAxis("2026-10-08 14:55")},           // 最后一根还没收盘
		{"2026-10-08 15:00", toAxis("2026-10-08 14:55").Add(step)}, // 收盘
		{"2026-10-09 00:00", toAxis("2026-10-09 09:30")},           // 午夜
		{"2026-10-12 09:29", toAxis("2026-10-12 09:30")},           // 周一开盘前
		{"2026-10-12 09:30", toAxis("2026-10-09 14:55").Add(step)}, // 周一开盘 = 上周五收盘之后
		{"2026-10-08 12:59", toAxis("2026-10-08 11:25").Add(step)}, // 午休将尽
		{"2026-10-08 11:31", toAxis("2026-10-08 13:00")},           // 午休开始
		{"2026-10-10 12:00", toAxis("2026-10-09 14:55").Add(step)}, // 周六
		{"2026-10-07 23:59", toAxis("2026-09-30 14:55").Add(step)}, // 节假日最后一天
		{"2026-10-08 10:00", toAxis("2026-10-08 10:00")},           // 盘中桶起点
		{"2026-10-08 10:03", toAxis("2026-10-08 10:00")},           // 盘中
		{"2026-10-08 10:05", toAxis("2026-10-08 10:05")},           // 盘中下一根起点
		{"2026-10-08 13:02", toAxis("2026-10-08 13:00")},           // 下午第一根盘中
		{"2026-10-08 11:29", toAxis("2026-10-08 11:25")},           // 上午最后一根盘中
		{"2026-10-08 14:00", toAxis("2026-10-08 14:00")},           // 下午桶起点
	} {
		got, err := axis.AxisEnd(cst(t, c.now))
		require.NoError(t, err, c.now)
		require.Equal(t, c.want, got, c.now)
	}
}

func TestSessionAxisEndRoundTrip(t *testing.T) {
	axis := stockAxis(t)
	// 排他终点换回真实时间是最后一根的收盘时刻，不跨到下一个交易日。
	end, err := axis.AxisEnd(cst(t, "2026-09-30 20:00"))
	require.NoError(t, err)
	closedAt, err := axis.FromAxisEnd(end)
	require.NoError(t, err)
	require.Equal(t, cst(t, "2026-09-30 15:00"), closedAt)

	// 周期边界都能作为排他终点：桶的起点或收盘时刻；午休中间的时刻不行。
	for value, want := range map[string]string{"2026-10-08 11:30": "2026-10-08 13:00", "2026-10-08 13:00": "2026-10-08 13:00", "2026-10-08 15:00": "2026-10-09 09:30"} {
		x, err := axis.EndToAxis(cst(t, value))
		require.NoError(t, err, value)
		w, err := axis.ToAxis(cst(t, want))
		require.NoError(t, err)
		require.Equal(t, w, x, value)
	}
	_, err = axis.EndToAxis(cst(t, "2026-10-08 12:00"))
	require.Error(t, err)
	require.Equal(t, "2026-10-08T05:00:00Z", axis.Format(mustAxis(t, axis, "2026-10-08 13:00")))
}

func mustAxis(t *testing.T, axis Axis, value string) time.Time {
	t.Helper()
	x, err := axis.ToAxis(cst(t, value))
	require.NoError(t, err)
	return x
}

func TestSessionWindowCountsTradingBars(t *testing.T) {
	clock, err := ForSpace("stockcn")
	require.NoError(t, err)
	got, err := clock.Window(cst(t, "2026-10-08 13:05"), "5m", 4)
	require.NoError(t, err)
	require.Equal(t, []time.Time{cst(t, "2026-10-08 11:20"), cst(t, "2026-10-08 11:25"), cst(t, "2026-10-08 13:00"), cst(t, "2026-10-08 13:05")}, got)
	_, err = clock.Align(cst(t, "2026-10-08 12:00"), "5m")
	require.Error(t, err)
}

func TestContinuousAxisIsIdentity(t *testing.T) {
	axis, err := AxisFor("crypto", "5m")
	require.NoError(t, err)
	require.True(t, axis.Identity())
	at := time.Date(2026, 10, 8, 3, 25, 0, 0, time.UTC)
	x, err := axis.ToAxis(at)
	require.NoError(t, err)
	require.Equal(t, at, x)
	back, err := axis.FromAxis(x)
	require.NoError(t, err)
	require.Equal(t, at, back)
	end, err := axis.AxisEnd(at.Add(3 * time.Minute))
	require.NoError(t, err)
	require.Equal(t, at, end, "向下对齐")
	closedAt, err := axis.FromAxisEnd(at)
	require.NoError(t, err)
	require.Equal(t, at, closedAt)
	_, err = axis.EndToAxis(at.Add(time.Minute))
	require.Error(t, err)
}

// 交易日历最后一天的收盘时刻也能作排他终点：换算不需要日历之外的下一根。
func TestSessionEndOfCalendar(t *testing.T) {
	axis := stockAxis(t)
	end, err := axis.EndToAxis(cst(t, "2026-12-31 15:00"))
	require.NoError(t, err)
	closedAt, err := axis.FromAxisEnd(end)
	require.NoError(t, err)
	require.Equal(t, cst(t, "2026-12-31 15:00"), closedAt)
	_, err = axis.FromAxis(end)
	require.Error(t, err, "下一根在日历之外")
}

// 换算是热路径（同步 Storage 时每行都换）：经 Axis 换算不分配内存。
func BenchmarkSessionAxisRoundTrip(b *testing.B) {
	axis, err := AxisFor("stockcn", "5m")
	require.NoError(b, err)
	at := time.Date(2026, 10, 8, 5, 5, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		x, err := axis.ToAxis(at)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := axis.FromAxis(x); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSessionAxisRoundTripDoesNotAllocate(t *testing.T) {
	axis := stockAxis(t)
	at := cst(t, "2026-10-08 13:05")
	allocs := testing.AllocsPerRun(100, func() {
		x, _ := axis.ToAxis(at)
		_, _ = axis.FromAxis(x)
	})
	require.Zero(t, allocs)
}
