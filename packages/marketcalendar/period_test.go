package marketcalendar

import (
	"errors"
	"testing"
)

// 节假日后的第一个交易日：2025 年国庆后 10-09（周四）是当周第 1 个、当周共 2 个；10 月共 17 个交易日。
func TestPeriodPositionAfterHoliday(t *testing.T) {
	calendar, err := Load("cn_stock")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	cases := []struct {
		date            string
		period          Period
		position, total int
	}{
		{"2025-10-09", PeriodWeek, 1, 2},
		{"2025-10-10", PeriodWeek, 2, 2},
		{"2025-10-13", PeriodWeek, 1, 5},
		{"2025-10-09", PeriodMonth, 1, 17},
		{"2025-10-31", PeriodMonth, 17, 17},
		{"2025-02-28", PeriodMonth, 18, 18},
	}
	for _, tc := range cases {
		position, total, err := calendar.PeriodPosition(mustDate(t, tc.date), tc.period)
		if err != nil || position != tc.position || total != tc.total {
			t.Fatalf("PeriodPosition(%s, %d) = %d/%d %v，应为 %d/%d", tc.date, tc.period, position, total, err, tc.position, tc.total)
		}
	}
	if _, _, err := calendar.PeriodPosition(mustDate(t, "2025-10-01"), PeriodWeek); !errors.Is(err, ErrNotTradingDay) {
		t.Fatalf("非交易日应返回 ErrNotTradingDay：%v", err)
	}
	if got := WeekStart(mustDate(t, "2025-10-12")); got.String() != "2025-10-06" {
		t.Fatalf("WeekStart(周日) = %s，应为当周周一", got)
	}
}

// 单周最多 5 个交易日；单月最多的交易日数从日历数据推出（A 股不超过 23）。
func TestMaxPeriodTradingDays(t *testing.T) {
	calendar, err := Load("cn_stock")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := calendar.MaxPeriodTradingDays(PeriodWeek); got != 5 {
		t.Fatalf("单周最多交易日 = %d，应为 5", got)
	}
	if got := calendar.MaxPeriodTradingDays(PeriodMonth); got < 21 || got > 23 {
		t.Fatalf("单月最多交易日 = %d，应在 21～23", got)
	}
}
