package marketcalendar

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 港股日历 hk_stock：按港交所公布的 2026、2027 年假期表生成，覆盖 2026-01-02 至 2027-12-31。
func TestLoadEmbeddedHongKongCalendar(t *testing.T) {
	t.Parallel()

	calendar, err := Load("hk_stock")
	if err != nil {
		t.Fatalf("Load() 出错：%v", err)
	}
	if calendar.FirstDate().String() != "2026-01-02" || calendar.LastDate().String() != "2027-12-31" {
		t.Fatalf("覆盖期 = %s..%s，期望 2026-01-02..2027-12-31", calendar.FirstDate(), calendar.LastDate())
	}
	manifest, err := parseCalendarManifest(embeddedHKManifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.CalendarID != "hk_stock" || !strings.Contains(manifest.Source, "hkex.com.hk") {
		t.Fatalf("清单 = %+v", manifest)
	}

	// 交易日数：两年的工作日都是 261 天，2026 年去掉 14 个休市日，2027 年去掉 13 个。
	counts := map[int]int{}
	for index := 0; ; index++ {
		day, err := calendar.TradingDayAt(index)
		if err != nil {
			break
		}
		counts[day.Year()]++
	}
	if counts[2026] != 247 || counts[2027] != 248 {
		t.Fatalf("交易日数 = %v，期望 2026 年 247、2027 年 248", counts)
	}

	for _, test := range []struct {
		date string
		want CoverageStatus
	}{
		{"2026-02-16", TradingDay},    // 农历新年前夕，半日市
		{"2026-02-17", NonTradingDay}, // 年初一
		{"2026-04-07", NonTradingDay}, // 复活节星期一翌日
		{"2026-05-25", NonTradingDay}, // 佛诞翌日
		{"2026-10-19", NonTradingDay}, // 重阳节翌日
		{"2026-10-20", TradingDay},
		{"2026-12-24", TradingDay},    // 圣诞前夕，半日市
		{"2026-12-25", NonTradingDay}, // 圣诞节
		{"2026-12-28", TradingDay},    // 节礼日是周六，不补假
		{"2027-02-05", TradingDay},    // 农历新年前夕，半日市
		{"2027-02-08", NonTradingDay}, // 年初三
		{"2027-02-09", NonTradingDay}, // 年初四
		{"2027-02-10", TradingDay},
		{"2027-09-16", NonTradingDay}, // 中秋节翌日
		{"2027-12-27", NonTradingDay}, // 圣诞节后第一个工作日
		{"2027-12-31", TradingDay},
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

// 提前收市日（半日市）：港股是圣诞前夕、新年前夕、农历新年前夕，12:00 收市；A 股没有。
func TestEarlyClose(t *testing.T) {
	t.Parallel()

	hk, err := Load("hk_stock")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-02-16", "2026-12-24", "2026-12-31", "2027-02-05", "2027-12-24", "2027-12-31"}
	dates := hk.EarlyCloseDates()
	if len(dates) != len(want) {
		t.Fatalf("EarlyCloseDates() = %v，期望 %v", dates, want)
	}
	for index, date := range dates {
		if date.String() != want[index] {
			t.Fatalf("EarlyCloseDates()[%d] = %s，期望 %s", index, date, want[index])
		}
		if closeAt, ok := hk.EarlyClose(date); !ok || closeAt != 12*time.Hour {
			t.Fatalf("EarlyClose(%s) = %v, %v，期望 12:00", date, closeAt, ok)
		}
	}
	for _, date := range []string{"2026-12-23", "2026-12-25", "2026-12-26"} {
		if _, ok := hk.EarlyClose(mustDate(t, date)); ok {
			t.Fatalf("%s 不是提前收市日", date)
		}
	}

	cn := mustLoad(t)
	if dates := cn.EarlyCloseDates(); len(dates) != 0 {
		t.Fatalf("cn_stock 不应有提前收市日：%v", dates)
	}
	if _, ok := cn.EarlyClose(mustDate(t, "2026-12-24")); ok {
		t.Fatal("cn_stock 不应有提前收市日")
	}
}

func TestParseCalendarDataValidatesEarlyCloses(t *testing.T) {
	t.Parallel()

	good := `{"calendar_id":"x","trading_days":["2024-01-02","2024-01-03"],"early_closes":{"2024-01-03":"12:00"}}`
	data, err := parseCalendarData([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(data.earlyCloseDates) != 1 || data.earlyCloses[data.earlyCloseDates[0]] != 12*time.Hour {
		t.Fatalf("提前收市日 = %v", data.earlyCloses)
	}
	for _, bad := range []string{
		`{"calendar_id":"x","trading_days":["2024-01-02"],"early_closes":{"2024-01-06":"12:00"}}`, // 不是交易日
		`{"calendar_id":"x","trading_days":["2024-01-02"],"early_closes":{"2024-01-02":"noon"}}`,
		`{"calendar_id":"x","trading_days":["2024-01-02"],"early_closes":{"2024-01-02":"00:00"}}`,
		`{"calendar_id":"x","trading_days":["2024-01-02"],"early_closes":{"20240102":"12:00"}}`,
	} {
		if _, err := parseCalendarData([]byte(bad)); !errors.Is(err, ErrInvalidCalendarData) {
			t.Fatalf("应拒绝 %s：%v", bad, err)
		}
	}
}

// 每份日历各自的清单与数据一致，日历之间互不依赖。
func TestEveryEmbeddedCalendarLoads(t *testing.T) {
	t.Parallel()

	for id := range embeddedCalendars {
		calendar, err := Load(id)
		if err != nil {
			t.Fatalf("Load(%q) 出错：%v", id, err)
		}
		if calendar.data.calendarID != id {
			t.Fatalf("Load(%q) 得到的是 %q", id, calendar.data.calendarID)
		}
	}
}
