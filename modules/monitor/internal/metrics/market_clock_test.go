package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

// fakeTradingDays 是覆盖 [from, through] 的测试日历：周一至周五交易，holidays 休市。
type fakeTradingDays struct {
	from, through string
	holidays      map[string]bool
}

func (d fakeTradingDays) Status(date marketcalendar.CivilDate) (marketcalendar.CoverageStatus, error) {
	value := date.String()
	if value < d.from || value > d.through {
		return marketcalendar.OutOfCoverage, errors.New("out of coverage")
	}
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if d.holidays[value] || day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		return marketcalendar.NonTradingDay, nil
	}
	return marketcalendar.TradingDay, nil
}

func stockClock(t *testing.T, days fakeTradingDays) MarketClock {
	t.Helper()
	clock, err := NewExchangeClock(days, shanghai, []string{"09:30-11:30", "13:00-15:00"})
	if err != nil {
		t.Fatal(err)
	}
	return clock
}

func at(day string, clock string) time.Time {
	value, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, shanghai)
	if err != nil {
		panic(err)
	}
	return value
}

func requireBar(t *testing.T, got Bar, err error, open, close time.Time) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Open.Equal(open) || !got.Close.Equal(close) {
		t.Fatalf("bar = %s ~ %s, want %s ~ %s", got.Open.In(shanghai), got.Close.In(shanghai), open.In(shanghai), close.In(shanghai))
	}
}

// 2026-10-16 是周五，2026-10-19 是周一。
var october = fakeTradingDays{from: "2026-09-01", through: "2026-12-31", holidays: map[string]bool{"2026-10-01": true, "2026-10-02": true, "2026-10-05": true, "2026-10-06": true, "2026-10-07": true}}

func TestStockMinuteClockFollowsSessions(t *testing.T) {
	clock := stockClock(t, october)
	cases := []struct {
		name       string
		now        time.Time
		open, shut string
		day        string
	}{
		// 午休：应收的最新一根是上午最后一根（11:29 开盘、11:30 收线）。
		{"午休 12:10", at("2026-10-16", "12:10:00"), "11:29:00", "11:30:00", "2026-10-16"},
		// 下午第一根还没收线：E 仍指向上午最后一根，不需要“开盘预热”跳过。
		{"13:00:30", at("2026-10-16", "13:00:30"), "11:29:00", "11:30:00", "2026-10-16"},
		{"13:01:05 下午第一根已收线", at("2026-10-16", "13:01:05"), "13:00:00", "13:01:00", "2026-10-16"},
		{"时段中", at("2026-10-16", "10:15:42"), "10:14:00", "10:15:00", "2026-10-16"},
		{"收盘后", at("2026-10-16", "20:00:00"), "14:59:00", "15:00:00", "2026-10-16"},
		// 周一开盘前回溯到上周五的最后一根。
		{"周一 09:00", at("2026-10-19", "09:00:00"), "14:59:00", "15:00:00", "2026-10-16"},
		// 国庆长假：10-08 开盘前回溯到 09-30 的最后一根。
		{"节假日", at("2026-10-08", "08:00:00"), "14:59:00", "15:00:00", "2026-09-30"},
		{"节假日当天", at("2026-10-05", "11:00:00"), "14:59:00", "15:00:00", "2026-09-30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clock.LatestClosed(tc.now, frequency.Minute1)
			requireBar(t, got, err, at(tc.day, tc.open), at(tc.day, tc.shut))
		})
	}
}

func TestStockMinuteClockWalksAcrossBreaks(t *testing.T) {
	clock := stockClock(t, october)
	lastMorning, err := clock.BarAt(at("2026-10-16", "11:29:00"), frequency.Minute1)
	requireBar(t, lastMorning, err, at("2026-10-16", "11:29:00"), at("2026-10-16", "11:30:00"))
	afternoon, err := clock.Next(lastMorning, frequency.Minute1)
	requireBar(t, afternoon, err, at("2026-10-16", "13:00:00"), at("2026-10-16", "13:01:00"))
	friday, err := clock.BarAt(at("2026-10-16", "14:59:00"), frequency.Minute1)
	requireBar(t, friday, err, at("2026-10-16", "14:59:00"), at("2026-10-16", "15:00:00"))
	monday, err := clock.Next(friday, frequency.Minute1)
	requireBar(t, monday, err, at("2026-10-19", "09:30:00"), at("2026-10-19", "09:31:00"))
	beforeHoliday, err := clock.BarAt(at("2026-09-30", "14:59:00"), frequency.Minute1)
	requireBar(t, beforeHoliday, err, at("2026-09-30", "14:59:00"), at("2026-09-30", "15:00:00"))
	afterHoliday, err := clock.Next(beforeHoliday, frequency.Minute1)
	requireBar(t, afterHoliday, err, at("2026-10-08", "09:30:00"), at("2026-10-08", "09:31:00"))

	// 午休中缺的只算上午已收线的那几根，不把午休时间当成缺失。
	latest, err := clock.BarAt(at("2026-10-16", "11:26:00"), frequency.Minute1)
	if err != nil {
		t.Fatal(err)
	}
	missing, first, err := clock.ClosedBarsAfter(latest, at("2026-10-16", "12:10:00"), frequency.Minute1)
	if err != nil || missing != 3 {
		t.Fatalf("missing = %d err = %v, want 3 (11:27/11:28/11:29)", missing, err)
	}
	requireBar(t, first, nil, at("2026-10-16", "11:27:00"), at("2026-10-16", "11:28:00"))

	if _, err := clock.BarAt(at("2026-10-16", "12:00:00"), frequency.Minute1); !errors.Is(err, ErrOutsideSession) {
		t.Fatalf("a lunch-break data time must not be guessed into a bar: %v", err)
	}
}

func TestStockDailyClockClosesAtTheLastSession(t *testing.T) {
	clock := stockClock(t, october)
	got, err := clock.LatestClosed(at("2026-10-16", "14:00:00"), frequency.Day1)
	requireBar(t, got, err, at("2026-10-15", "00:00:00"), at("2026-10-15", "15:00:00"))
	got, err = clock.LatestClosed(at("2026-10-19", "09:00:00"), frequency.Day1)
	requireBar(t, got, err, at("2026-10-16", "00:00:00"), at("2026-10-16", "15:00:00"))
	got, err = clock.BarAt(at("2026-10-16", "00:00:00"), frequency.Day1)
	requireBar(t, got, err, at("2026-10-16", "00:00:00"), at("2026-10-16", "15:00:00"))
	next, err := clock.Next(got, frequency.Day1)
	requireBar(t, next, err, at("2026-10-19", "00:00:00"), at("2026-10-19", "15:00:00"))
	// 周线收在本周最后一个交易日；国庆所在周只有 09-28～09-30 交易。
	week, err := clock.BarAt(at("2026-09-28", "00:00:00"), frequency.Week1)
	requireBar(t, week, err, at("2026-09-28", "00:00:00"), at("2026-09-30", "15:00:00"))
}

func TestStockClockReportsCalendarCoverageInsteadOfGuessing(t *testing.T) {
	expiring := fakeTradingDays{from: "2026-09-01", through: "2026-10-16"}
	clock := stockClock(t, expiring)
	if _, err := clock.LatestClosed(at("2026-10-19", "10:00:00"), frequency.Minute1); !errors.Is(err, ErrCalendarNotCovered) {
		t.Fatalf("a date beyond coverage must report 日历未覆盖, got %v", err)
	}
	latest, err := clock.BarAt(at("2026-10-16", "14:59:00"), frequency.Minute1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clock.Next(latest, frequency.Minute1); !errors.Is(err, ErrCalendarNotCovered) {
		t.Fatalf("the next bar after coverage must report 日历未覆盖, got %v", err)
	}
}

func TestStockIntradayClockSplitsEachSession(t *testing.T) {
	clock := stockClock(t, october)
	// 1h：09:30～10:30、10:30～11:30、13:00～14:00、14:00～15:00，10:45 时最新收线的是 09:30～10:30。
	got, err := clock.LatestClosed(at("2026-10-16", "10:45:00"), frequency.Hour1)
	requireBar(t, got, err, at("2026-10-16", "09:30:00"), at("2026-10-16", "10:30:00"))
	// 午休时最新一根是上午最后一根；下一根是下午第一根，不跨午休。
	got, err = clock.LatestClosed(at("2026-10-16", "12:00:00"), frequency.Hour1)
	requireBar(t, got, err, at("2026-10-16", "10:30:00"), at("2026-10-16", "11:30:00"))
	got, err = clock.Next(got, frequency.Hour1)
	requireBar(t, got, err, at("2026-10-16", "13:00:00"), at("2026-10-16", "14:00:00"))
	// 30m 的 BarAt：下午 13:40 属于 13:30 开盘的那一根。
	got, err = clock.BarAt(at("2026-10-16", "13:40:00"), frequency.Minute30)
	requireBar(t, got, err, at("2026-10-16", "13:30:00"), at("2026-10-16", "14:00:00"))
	// 周五收盘后的下一根是下周一开盘。
	got, err = clock.Next(Bar{Open: at("2026-10-16", "14:00:00"), Close: at("2026-10-16", "15:00:00")}, frequency.Hour1)
	requireBar(t, got, err, at("2026-10-19", "09:30:00"), at("2026-10-19", "10:30:00"))
}

func TestStockIntradayClockRejectsPeriodsThatDoNotDivideSessions(t *testing.T) {
	clock := stockClock(t, october)
	for _, freq := range []frequency.Frequency{"45m", frequency.Hour4} {
		if _, err := clock.LatestClosed(at("2026-10-16", "10:45:00"), freq); !errors.Is(err, ErrAlignmentUnconfirmed) {
			t.Fatalf("%s does not divide the sessions and must be reported as unconfirmed, got %v", freq, err)
		}
	}
}

func TestCryptoClockRoundsToThePeriod(t *testing.T) {
	clock := ContinuousClock()
	now := time.Date(2026, 10, 16, 6, 10, 5, 0, time.UTC)
	got, err := clock.LatestClosed(now, frequency.Minute1)
	requireBar(t, got, err, time.Date(2026, 10, 16, 6, 9, 0, 0, time.UTC), time.Date(2026, 10, 16, 6, 10, 0, 0, time.UTC))
	got, err = clock.LatestClosed(now, frequency.Hour1)
	requireBar(t, got, err, time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC))
	got, err = clock.LatestClosed(now, frequency.Hour4)
	requireBar(t, got, err, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC))
	// 2026-10-16 是周五：上一根周线是 10-05（周一）至 10-12。
	got, err = clock.LatestClosed(now, frequency.Week1)
	requireBar(t, got, err, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	got, err = clock.LatestClosed(now, frequency.Month1)
	requireBar(t, got, err, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	// 周末同样按周期取整，不回溯。
	saturday := time.Date(2026, 10, 17, 3, 0, 30, 0, time.UTC)
	got, err = clock.LatestClosed(saturday, frequency.Minute1)
	requireBar(t, got, err, time.Date(2026, 10, 17, 2, 59, 0, 0, time.UTC), time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC))
	missing, first, err := clock.ClosedBarsAfter(Bar{Open: time.Date(2026, 10, 16, 6, 5, 0, 0, time.UTC), Close: time.Date(2026, 10, 16, 6, 6, 0, 0, time.UTC)}, now, frequency.Minute1)
	if err != nil || missing != 4 {
		t.Fatalf("missing = %d err = %v", missing, err)
	}
	requireBar(t, first, nil, time.Date(2026, 10, 16, 6, 6, 0, 0, time.UTC), time.Date(2026, 10, 16, 6, 7, 0, 0, time.UTC))
}

func TestMarketClockForUsesTheInventoryCalendar(t *testing.T) {
	if clock, err := MarketClockFor("crypto", "", "UTC", nil); err != nil || clock.exchange {
		t.Fatalf("crypto must be continuous: %+v %v", clock, err)
	}
	clock, err := MarketClockFor("stockcn", "cn_stock", "Asia/Shanghai", []string{"09:30-11:30", "13:00-15:00"})
	if err != nil || !clock.exchange || len(clock.sessions) != 2 {
		t.Fatalf("stockcn clock = %+v err = %v", clock, err)
	}
	if _, err := MarketClockFor("stockus", "", "America/New_York", nil); !errors.Is(err, ErrNoTradingCalendar) {
		t.Fatalf("a market without calendar must not be guessed: %v", err)
	}
}
