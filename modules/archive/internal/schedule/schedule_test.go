package schedule

import (
	"testing"
	"time"
)

func utc(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestTickBoundaryPerFrequency(t *testing.T) {
	sh, ny := mustLoc(t, "Asia/Shanghai"), mustLoc(t, "America/New_York")
	for _, tc := range []struct {
		name   string
		timing Timing
		now    time.Time
		want   time.Time
	}{
		{"1m", Timing{Freq: "1m"}, utc(2026, 10, 17, 12, 40, 25), utc(2026, 10, 17, 12, 40, 0)},
		{"1m N=5", Timing{Freq: "1m", PullEveryBars: 5}, utc(2026, 10, 17, 12, 44, 25), utc(2026, 10, 17, 12, 40, 0)},
		{"5m", Timing{Freq: "5m"}, utc(2026, 10, 17, 12, 44, 25), utc(2026, 10, 17, 12, 40, 0)},
		{"5m N=3", Timing{Freq: "5m", PullEveryBars: 3}, utc(2026, 10, 17, 12, 44, 25), utc(2026, 10, 17, 12, 30, 0)},
		{"1h", Timing{Freq: "1h"}, utc(2026, 10, 17, 13, 4, 25), utc(2026, 10, 17, 13, 0, 0)},
		{"4h", Timing{Freq: "4h"}, utc(2026, 10, 17, 11, 59, 25), utc(2026, 10, 17, 8, 0, 0)},
		{"4h N=2", Timing{Freq: "4h", PullEveryBars: 2}, utc(2026, 10, 17, 11, 59, 25), utc(2026, 10, 17, 8, 0, 0)},
		{"crypto 1d", Timing{Freq: "1d", Loc: time.UTC}, utc(2026, 10, 17, 0, 4, 25), utc(2026, 10, 17, 0, 0, 0)},
		{"1d 忽略 N", Timing{Freq: "1d", PullEveryBars: 7}, utc(2026, 10, 17, 23, 59, 0), utc(2026, 10, 17, 0, 0, 0)},
		{"1w 每天一个边界", Timing{Freq: "1w"}, utc(2026, 10, 17, 3, 0, 0), utc(2026, 10, 17, 0, 0, 0)},
		{"1mo 每天一个边界", Timing{Freq: "1mo"}, utc(2026, 10, 17, 3, 0, 0), utc(2026, 10, 17, 0, 0, 0)},
		// A 股 5m 按 UTC 对齐，与交易时段无关。
		{"stockcn 5m", Timing{Freq: "5m", Loc: sh, LocalClose: "15:00"}, utc(2026, 10, 17, 7, 3, 25), utc(2026, 10, 17, 7, 0, 0)},
		// A 股 1d：上海 15:00（07:00 UTC）收盘，收盘前取前一天的收盘。
		{"stockcn 1d 收盘后", Timing{Freq: "1d", Loc: sh, LocalClose: "15:00"}, utc(2026, 10, 17, 7, 0, 25), utc(2026, 10, 17, 7, 0, 0)},
		{"stockcn 1d 收盘前", Timing{Freq: "1d", Loc: sh, LocalClose: "15:00"}, utc(2026, 10, 17, 6, 59, 25), utc(2026, 10, 16, 7, 0, 0)},
		// 跨日：上海 10-18 00:30（UTC 10-17 16:30）仍取 10-17 15:00。
		{"stockcn 1d 跨日", Timing{Freq: "1d", Loc: sh, LocalClose: "15:00"}, utc(2026, 10, 17, 16, 30, 25), utc(2026, 10, 17, 7, 0, 0)},
		// 美股 16:00 收盘：夏令时 20:00 UTC，夏令时结束后 21:00 UTC。
		{"stockus 夏令时", Timing{Freq: "1d", Loc: ny, LocalClose: "16:00"}, utc(2026, 10, 30, 22, 0, 0), utc(2026, 10, 30, 20, 0, 0)},
		{"stockus 冬令时", Timing{Freq: "1d", Loc: ny, LocalClose: "16:00"}, utc(2026, 11, 2, 22, 0, 0), utc(2026, 11, 2, 21, 0, 0)},
	} {
		got, err := TickBoundary(tc.timing, tc.now)
		if err != nil || !got.Equal(tc.want) {
			t.Fatalf("%s: TickBoundary(%s) = %s, %v, want %s", tc.name, tc.now, got, err, tc.want)
		}
	}
}

func TestTimingRejectsBadInput(t *testing.T) {
	for name, timing := range map[string]Timing{
		"未知频率":  {Freq: "7x"},
		"N 过大":  {Freq: "1m", PullEveryBars: 1441},
		"N 为负":  {Freq: "1m", PullEveryBars: -1},
		"收盘时刻错": {Freq: "1d", LocalClose: "25:99"},
	} {
		if _, err := TickBoundary(timing, utc(2026, 10, 17, 0, 0, 0)); err == nil {
			t.Fatalf("%s: 应报错", name)
		}
	}
	if ValidatePullEveryBars(0) == nil || ValidatePullEveryBars(1440) != nil {
		t.Fatal("N 的范围是 1～1440")
	}
}

func TestReviewBoundaryAndInterval(t *testing.T) {
	if got, _ := ReviewBoundary(Timing{Freq: "1m"}, utc(2026, 10, 17, 12, 34, 25)); !got.Equal(utc(2026, 10, 17, 12, 34, 0)) {
		t.Fatalf("12:34:25 的复核边界 = %s", got)
	}
	if got, _ := ReviewBoundary(Timing{Freq: "5m"}, utc(2026, 10, 17, 12, 33, 25)); !got.Equal(utc(2026, 10, 17, 11, 34, 0)) {
		t.Fatalf("12:33:25 的复核边界 = %s", got)
	}
	if got, _ := ReviewBoundary(Timing{Freq: "1d"}, utc(2026, 10, 17, 12, 33, 25)); !got.Equal(utc(2026, 10, 17, 0, 0, 0)) {
		t.Fatalf("1d 的复核边界即拉取边界: %s", got)
	}
	for freq, want := range map[string]time.Duration{"1m": time.Hour, "4h": time.Hour, "1d": 24 * time.Hour, "1w": 24 * time.Hour, "1mo": 24 * time.Hour} {
		if got, err := ReviewInterval(freq); err != nil || got != want {
			t.Fatalf("ReviewInterval(%s) = %s, %v", freq, got, err)
		}
	}
}

func TestPullAtShiftsOutOfTheAvoidanceWindow(t *testing.T) {
	for _, tc := range []struct{ boundary, want time.Time }{
		{utc(2026, 10, 17, 12, 40, 0), utc(2026, 10, 17, 12, 40, 25)},
		{utc(2026, 10, 17, 12, 40, 30), utc(2026, 10, 17, 12, 41, 25)}, // 30s 边界落到下一分钟的第 25 秒
		{utc(2026, 10, 17, 12, 57, 0), utc(2026, 10, 17, 12, 57, 25)},
		{utc(2026, 10, 17, 12, 58, 0), utc(2026, 10, 17, 13, 4, 25)},
		{utc(2026, 10, 17, 13, 0, 0), utc(2026, 10, 17, 13, 4, 25)},
		{utc(2026, 10, 17, 13, 3, 0), utc(2026, 10, 17, 13, 4, 25)},
		{utc(2026, 10, 17, 13, 4, 0), utc(2026, 10, 17, 13, 4, 25)},
		{utc(2026, 10, 17, 23, 59, 0), utc(2026, 10, 18, 0, 4, 25)}, // 跨日
	} {
		if got := PullAt(tc.boundary); !got.Equal(tc.want) {
			t.Fatalf("PullAt(%s) = %s, want %s", tc.boundary, got, tc.want)
		}
	}
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{utc(2026, 10, 17, 12, 57, 59), false}, {utc(2026, 10, 17, 12, 58, 0), true}, {utc(2026, 10, 17, 13, 0, 25), true},
		{utc(2026, 10, 17, 13, 3, 59), true}, {utc(2026, 10, 17, 13, 4, 0), false},
	} {
		if got := InAvoidance(tc.at); got != tc.want {
			t.Fatalf("InAvoidance(%s) = %v", tc.at, got)
		}
	}
}

func TestNextPullsAndReviews(t *testing.T) {
	pulls := func(timing Timing, now time.Time, n int) []time.Time {
		t.Helper()
		got, err := NextPulls(timing, now, n)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	equal := func(name string, got []time.Time, want ...time.Time) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range want {
			if !got[i].Equal(want[i]) {
				t.Fatalf("%s[%d]: got %v, want %v", name, i, got, want)
			}
		}
	}
	// 1m：每分钟第 25 秒；避让区间里的边界合并到 :04:25。
	equal("1m", pulls(Timing{Freq: "1m"}, utc(2026, 10, 17, 12, 56, 0), 4),
		utc(2026, 10, 17, 12, 56, 25), utc(2026, 10, 17, 12, 57, 25), utc(2026, 10, 17, 13, 4, 25), utc(2026, 10, 17, 13, 5, 25))
	// 恰好在检查时刻：这一次已经算过去了。
	equal("1m 检查时刻", pulls(Timing{Freq: "1m"}, utc(2026, 10, 17, 12, 40, 25), 1), utc(2026, 10, 17, 12, 41, 25))
	equal("5m N=2", pulls(Timing{Freq: "5m", PullEveryBars: 2}, utc(2026, 10, 17, 12, 41, 0), 3),
		utc(2026, 10, 17, 12, 50, 25), utc(2026, 10, 17, 13, 4, 25), utc(2026, 10, 17, 13, 10, 25))
	// 1h：整点在避让区间里，每小时 :04:25。
	equal("1h", pulls(Timing{Freq: "1h"}, utc(2026, 10, 17, 12, 10, 0), 2), utc(2026, 10, 17, 13, 4, 25), utc(2026, 10, 17, 14, 4, 25))
	// 整点刚过、:04:25 之前：当前小时的边界还没拉。
	equal("1h 整点后", pulls(Timing{Freq: "1h"}, utc(2026, 10, 17, 13, 2, 0), 1), utc(2026, 10, 17, 13, 4, 25))
	equal("4h", pulls(Timing{Freq: "4h"}, utc(2026, 10, 17, 9, 0, 0), 2), utc(2026, 10, 17, 12, 4, 25), utc(2026, 10, 17, 16, 4, 25))
	equal("crypto 1d", pulls(Timing{Freq: "1d"}, utc(2026, 10, 17, 9, 0, 0), 2), utc(2026, 10, 18, 0, 4, 25), utc(2026, 10, 19, 0, 4, 25))
	sh := mustLoc(t, "Asia/Shanghai")
	// A 股 1d：15:00（07:00 UTC）收盘落在整点避让区间，15:04:25 拉取。
	equal("stockcn 1d", pulls(Timing{Freq: "1d", Loc: sh, LocalClose: "15:00"}, utc(2026, 10, 17, 9, 0, 0), 2), utc(2026, 10, 18, 7, 4, 25), utc(2026, 10, 19, 7, 4, 25))
	equal("stockcn 5m", pulls(Timing{Freq: "5m", Loc: sh, LocalClose: "15:00"}, utc(2026, 10, 17, 6, 51, 0), 3), utc(2026, 10, 17, 6, 55, 25), utc(2026, 10, 17, 7, 4, 25), utc(2026, 10, 17, 7, 5, 25))

	reviews, err := NextReviews(Timing{Freq: "1m"}, utc(2026, 10, 17, 12, 40, 0), 3)
	if err != nil {
		t.Fatal(err)
	}
	equal("1m 复核", reviews, utc(2026, 10, 17, 13, 34, 25), utc(2026, 10, 17, 14, 34, 25), utc(2026, 10, 17, 15, 34, 25))
	reviews, _ = NextReviews(Timing{Freq: "1m"}, utc(2026, 10, 17, 12, 20, 0), 1)
	equal("1m 复核同一小时", reviews, utc(2026, 10, 17, 12, 34, 25))
	reviews, _ = NextReviews(Timing{Freq: "1d"}, utc(2026, 10, 17, 9, 0, 0), 1)
	equal("1d 复核即拉取", reviews, utc(2026, 10, 18, 0, 4, 25))
}

func TestDecide(t *testing.T) {
	sh := mustLoc(t, "Asia/Shanghai")
	m1 := Timing{Freq: "1m"}
	for _, tc := range []struct {
		name               string
		timing             Timing
		lastBoundary, last time.Time
		now                time.Time
		want               Decision
	}{
		{
			name: "新任务首次检查即复核", timing: m1, now: utc(2026, 10, 17, 12, 40, 25),
			want: Decision{Action: ActionReview, LastBoundary: utc(2026, 10, 17, 12, 40, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "不到点", timing: Timing{Freq: "5m"}, lastBoundary: utc(2026, 10, 17, 12, 40, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 44, 25),
			want: Decision{},
		},
		{
			name: "1m 到点拉取读新收盘一根加 2 根", timing: m1, lastBoundary: utc(2026, 10, 17, 12, 39, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 40, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 12, 37, 0), RangeEnd: utc(2026, 10, 17, 12, 40, 25),
				LastBoundary: utc(2026, 10, 17, 12, 40, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "5m N=3 不到点", timing: Timing{Freq: "5m", PullEveryBars: 3}, lastBoundary: utc(2026, 10, 17, 12, 30, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 40, 25),
			want: Decision{},
		},
		{
			name: "5m N=3 到点读 N+2 根", timing: Timing{Freq: "5m", PullEveryBars: 3}, lastBoundary: utc(2026, 10, 17, 12, 30, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 45, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 12, 20, 0), RangeEnd: utc(2026, 10, 17, 12, 45, 25),
				LastBoundary: utc(2026, 10, 17, 12, 45, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "跳过几次后从更早的边界读起", timing: m1, lastBoundary: utc(2026, 10, 17, 12, 30, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 40, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 12, 28, 0), RangeEnd: utc(2026, 10, 17, 12, 40, 25),
				LastBoundary: utc(2026, 10, 17, 12, 40, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "落后 60 根以上只读最近 N+2 根", timing: m1, lastBoundary: utc(2026, 10, 17, 11, 39, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 40, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 12, 37, 0), RangeEnd: utc(2026, 10, 17, 12, 40, 25),
				LastBoundary: utc(2026, 10, 17, 12, 40, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "复核与拉取同时到点只生成复核", timing: m1, lastBoundary: utc(2026, 10, 17, 12, 33, 0), last: utc(2026, 10, 17, 11, 34, 0), now: utc(2026, 10, 17, 12, 34, 25),
			want: Decision{Action: ActionReview, LastBoundary: utc(2026, 10, 17, 12, 34, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "复核到点但拉取不到点", timing: Timing{Freq: "1h"}, lastBoundary: utc(2026, 10, 17, 12, 0, 0), last: utc(2026, 10, 17, 11, 34, 0), now: utc(2026, 10, 17, 12, 34, 25),
			want: Decision{Action: ActionReview, LastBoundary: utc(2026, 10, 17, 12, 0, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "1h 在 :04:25 拉取整点收盘的一根", timing: Timing{Freq: "1h"}, lastBoundary: utc(2026, 10, 17, 12, 0, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 13, 4, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 10, 0, 0), RangeEnd: utc(2026, 10, 17, 13, 4, 25),
				LastBoundary: utc(2026, 10, 17, 13, 0, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "4h 到点", timing: Timing{Freq: "4h"}, lastBoundary: utc(2026, 10, 17, 8, 0, 0), last: utc(2026, 10, 17, 11, 34, 0), now: utc(2026, 10, 17, 12, 4, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 0, 0, 0), RangeEnd: utc(2026, 10, 17, 12, 4, 25),
				LastBoundary: utc(2026, 10, 17, 12, 0, 0), LastReview: utc(2026, 10, 17, 11, 34, 0)},
		},
		{
			name: "避让区间整次跳过", timing: m1, lastBoundary: utc(2026, 10, 17, 12, 57, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 12, 59, 25),
			want: Decision{},
		},
		{
			name: "出避让区间第一次检查补上前进的边界", timing: m1, lastBoundary: utc(2026, 10, 17, 12, 57, 0), last: utc(2026, 10, 17, 12, 34, 0), now: utc(2026, 10, 17, 13, 4, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 12, 55, 0), RangeEnd: utc(2026, 10, 17, 13, 4, 25),
				LastBoundary: utc(2026, 10, 17, 13, 4, 0), LastReview: utc(2026, 10, 17, 12, 34, 0)},
		},
		{
			name: "crypto 1d 只生成复核", timing: Timing{Freq: "1d"}, lastBoundary: utc(2026, 10, 16, 0, 0, 0), last: utc(2026, 10, 16, 0, 0, 0), now: utc(2026, 10, 17, 0, 4, 25),
			want: Decision{Action: ActionReview, LastBoundary: utc(2026, 10, 17, 0, 0, 0), LastReview: utc(2026, 10, 17, 0, 0, 0)},
		},
		{
			name: "crypto 1d 当天不再触发", timing: Timing{Freq: "1d"}, lastBoundary: utc(2026, 10, 17, 0, 0, 0), last: utc(2026, 10, 17, 0, 0, 0), now: utc(2026, 10, 17, 12, 34, 25),
			want: Decision{},
		},
		{
			name: "stockcn 1d 收盘后第一次检查复核", timing: Timing{Freq: "1d", Loc: sh, LocalClose: "15:00"}, lastBoundary: utc(2026, 10, 16, 7, 0, 0), last: utc(2026, 10, 16, 7, 0, 0), now: utc(2026, 10, 17, 7, 4, 25),
			want: Decision{Action: ActionReview, LastBoundary: utc(2026, 10, 17, 7, 0, 0), LastReview: utc(2026, 10, 17, 7, 0, 0)},
		},
		{
			name: "stockcn 5m 按 UTC 对齐拉取", timing: Timing{Freq: "5m", Loc: sh, LocalClose: "15:00"}, lastBoundary: utc(2026, 10, 17, 7, 5, 0), last: utc(2026, 10, 17, 6, 34, 0), now: utc(2026, 10, 17, 7, 10, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 6, 55, 0), RangeEnd: utc(2026, 10, 17, 7, 10, 25),
				LastBoundary: utc(2026, 10, 17, 7, 10, 0), LastReview: utc(2026, 10, 17, 6, 34, 0)},
		},
		{
			// 00:04:25 时复核边界仍是 23:34，复核不到点，生成到点拉取。
			name: "跨日的到点拉取", timing: m1, lastBoundary: utc(2026, 10, 17, 23, 57, 0), last: utc(2026, 10, 17, 23, 34, 0), now: utc(2026, 10, 18, 0, 4, 25),
			want: Decision{Action: ActionTick, RangeStart: utc(2026, 10, 17, 23, 55, 0), RangeEnd: utc(2026, 10, 18, 0, 4, 25),
				LastBoundary: utc(2026, 10, 18, 0, 4, 0), LastReview: utc(2026, 10, 17, 23, 34, 0)},
		},
	} {
		got, err := Decide(tc.timing, tc.lastBoundary, tc.last, tc.now)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Action != tc.want.Action || !got.RangeStart.Equal(tc.want.RangeStart) || !got.RangeEnd.Equal(tc.want.RangeEnd) ||
			!got.LastBoundary.Equal(tc.want.LastBoundary) || !got.LastReview.Equal(tc.want.LastReview) {
			t.Fatalf("%s:\n got  %+v\n want %+v", tc.name, got, tc.want)
		}
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
	got, err := ReadSpan(time.Hour, 15*time.Minute, "1m")
	if err != nil || got != 76*time.Minute {
		t.Fatalf("ReadSpan(crypto 1m, 1h) = %s, %v, want 76m", got, err)
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

func TestPartitionCloseAndExportEstimate(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// stockcn 日分区：上海 15:00 收盘。
	key, start, end := PartitionOf("day", sh, time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC))
	closeAt, err := PartitionClose("day", sh, "15:00", "1m", start, end)
	if err != nil || key != "2026-10-17" || !closeAt.Equal(time.Date(2026, 10, 17, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("close = %s err=%v key=%s", closeAt, err, key)
	}
	// stockcn 1m：15:00 收盘 + 15 分钟 = 07:15 UTC，下一次到点拉取 07:15:25。
	if got, err := ExportEstimate(Timing{Freq: "1m", Loc: sh, LocalClose: "15:00"}, closeAt, 15*time.Minute); err != nil || !got.Equal(time.Date(2026, 10, 17, 7, 15, 25, 0, time.UTC)) {
		t.Fatalf("stockcn 导出预计 = %s err=%v", got, err)
	}
	// crypto 1h 日分区：00:15 之后第一次是 00:34:25 的复核（早于 01:04:25 的到点拉取）。
	utcKey, ustart, uend := PartitionOf("day", time.UTC, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	ucl, _ := PartitionClose("day", time.UTC, "", "1h", ustart, uend)
	if got, err := ExportEstimate(Timing{Freq: "1h"}, ucl, 15*time.Minute); err != nil || utcKey != "2026-10-16" || !got.Equal(time.Date(2026, 10, 17, 0, 34, 25, 0, time.UTC)) {
		t.Fatalf("crypto 1h 导出预计 = %s key=%s err=%v", got, utcKey, err)
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
