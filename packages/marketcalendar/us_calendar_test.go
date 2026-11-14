package marketcalendar

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 美股日历 us_stock：按纽交所公布的 2026、2027 年假期表生成，覆盖 2026-01-02 至 2027-12-31。
func TestLoadEmbeddedUSCalendar(t *testing.T) {
	t.Parallel()

	calendar, err := Load("us_stock")
	if err != nil {
		t.Fatalf("Load() 出错：%v", err)
	}
	if calendar.FirstDate().String() != "2026-01-02" || calendar.LastDate().String() != "2027-12-31" {
		t.Fatalf("覆盖期 = %s..%s，期望 2026-01-02..2027-12-31", calendar.FirstDate(), calendar.LastDate())
	}
	manifest, err := parseCalendarManifest(embeddedUSManifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.CalendarID != "us_stock" || !strings.Contains(manifest.Source, "nyse.com") {
		t.Fatalf("清单 = %+v", manifest)
	}

	// 交易日数：两年的工作日都是 261 天，每年去掉 10 个休市日。
	counts := map[int]int{}
	for index := 0; ; index++ {
		day, err := calendar.TradingDayAt(index)
		if err != nil {
			break
		}
		counts[day.Year()]++
	}
	if counts[2026] != 251 || counts[2027] != 251 {
		t.Fatalf("交易日数 = %v，期望 2026 年 251、2027 年 251", counts)
	}

	for _, test := range []struct {
		date string
		want CoverageStatus
	}{
		{"2026-01-02", TradingDay},
		{"2026-01-19", NonTradingDay}, // 马丁·路德·金纪念日
		{"2026-04-03", NonTradingDay}, // 耶稣受难节
		{"2026-07-02", TradingDay},
		{"2026-07-03", NonTradingDay}, // 独立日补休（7-4 是周六）
		{"2026-11-26", NonTradingDay}, // 感恩节
		{"2026-11-27", TradingDay},    // 半日市
		{"2026-12-24", TradingDay},    // 半日市
		{"2026-12-25", NonTradingDay},
		{"2027-01-01", NonTradingDay},
		{"2027-03-26", NonTradingDay}, // 耶稣受难节
		{"2027-06-18", NonTradingDay}, // 六月节补休（6-19 是周六）
		{"2027-07-02", TradingDay},    // 7 月没有提前收市
		{"2027-07-05", NonTradingDay}, // 独立日补休（7-4 是周日）
		{"2027-11-25", NonTradingDay},
		{"2027-11-26", TradingDay}, // 半日市
		{"2027-12-23", TradingDay},
		{"2027-12-24", NonTradingDay}, // 圣诞节补休（12-25 是周六）
		{"2027-12-31", TradingDay},    // 官网没有列，照常交易
	} {
		if got, err := calendar.Status(mustDate(t, test.date)); err != nil || got != test.want {
			t.Fatalf("Status(%s) = %v，期望 %v（err=%v）", test.date, got, test.want, err)
		}
	}
	if _, err := calendar.Status(mustDate(t, "2026-01-01")); !errors.Is(err, ErrOutOfCoverage) {
		t.Fatalf("覆盖期之前应返回 ErrOutOfCoverage：%v", err)
	}
	if _, err := calendar.Status(mustDate(t, "2028-01-03")); !errors.Is(err, ErrOutOfCoverage) {
		t.Fatalf("覆盖期之后应返回 ErrOutOfCoverage：%v", err)
	}
}

// 美股的半日市：感恩节次日、2026 年平安夜，13:00 收市；2027 年平安夜休市，不是半日市。
func TestUSEarlyCloses(t *testing.T) {
	t.Parallel()

	us, err := Load("us_stock")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-11-27", "2026-12-24", "2027-11-26"}
	dates := us.EarlyCloseDates()
	if len(dates) != len(want) {
		t.Fatalf("EarlyCloseDates() = %v，期望 %v", dates, want)
	}
	for index, date := range dates {
		if date.String() != want[index] {
			t.Fatalf("EarlyCloseDates()[%d] = %s，期望 %s", index, date, want[index])
		}
		if closeAt, ok := us.EarlyClose(date); !ok || closeAt != 13*time.Hour {
			t.Fatalf("EarlyClose(%s) = %v, %v，期望 13:00", date, closeAt, ok)
		}
	}
	for _, date := range []string{"2026-07-02", "2026-12-23", "2026-12-28", "2027-07-02", "2027-12-23", "2027-12-31"} {
		if _, ok := us.EarlyClose(mustDate(t, date)); ok {
			t.Fatalf("%s 不是提前收市日", date)
		}
	}
}
