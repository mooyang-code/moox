package marketcalendar

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCivilDateIsAValidatedTimezoneFreeValue(t *testing.T) {
	t.Parallel()

	date, err := NewCivilDate(2024, time.February, 29)
	if err != nil {
		t.Fatalf("NewCivilDate() error = %v", err)
	}
	if got := date.String(); got != "2024-02-29" {
		t.Fatalf("CivilDate.String() = %q, want %q", got, "2024-02-29")
	}
	if date.Year() != 2024 || date.Month() != time.February || date.Day() != 29 {
		t.Fatalf("CivilDate components = %d-%d-%d", date.Year(), date.Month(), date.Day())
	}
	if date.IsZero() {
		t.Fatal("valid CivilDate is zero")
	}

	parsed, err := ParseCivilDate(date.String())
	if err != nil {
		t.Fatalf("ParseCivilDate() error = %v", err)
	}
	if parsed != date {
		t.Fatalf("ParseCivilDate() = %v, want %v", parsed, date)
	}
}

func TestCivilDateRejectsInvalidAndNonCanonicalValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "2024-2-01", "2024/02/01", "2024-02-30", "0000-01-01", "2024-02-01T00:00:00Z"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseCivilDate(value); !errors.Is(err, ErrInvalidCivilDate) {
				t.Fatalf("ParseCivilDate(%q) error = %v, want ErrInvalidCivilDate", value, err)
			}
		})
	}

	if _, err := NewCivilDate(2024, time.January, 0); !errors.Is(err, ErrInvalidCivilDate) {
		t.Fatalf("NewCivilDate() error = %v, want ErrInvalidCivilDate", err)
	}
}

func TestLoadEmbeddedChinaCalendarAndManifest(t *testing.T) {
	t.Parallel()

	calendar, err := Load("cn_stock")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	manifest := calendar.Manifest()
	if calendar.ID() != "cn_stock" || manifest.CalendarID != "cn_stock" {
		t.Fatalf("calendar identity = %q/%q", calendar.ID(), manifest.CalendarID)
	}
	if manifest.Version == 0 {
		t.Fatal("manifest version is zero")
	}
	if strings.TrimSpace(manifest.Source) == "" {
		t.Fatal("manifest source is empty")
	}
	if !strings.HasPrefix(manifest.SHA256, "sha256:") || len(manifest.SHA256) != len("sha256:")+sha256.Size*2 {
		t.Fatalf("manifest SHA256 = %q", manifest.SHA256)
	}
	if calendar.FirstDate().String() != "1990-12-19" {
		t.Fatalf("FirstDate() = %s, want 1990-12-19", calendar.FirstDate())
	}
	if calendar.LastDate().String() != "2026-12-31" {
		t.Fatalf("LastDate() = %s, want 2026-12-31", calendar.LastDate())
	}
	if manifest.ValidFrom != calendar.FirstDate() || manifest.ValidThrough != calendar.LastDate() {
		t.Fatalf("manifest coverage = %s..%s, calendar = %s..%s", manifest.ValidFrom, manifest.ValidThrough, calendar.FirstDate(), calendar.LastDate())
	}
}

func TestEmbeddedDataHashMatchesManifest(t *testing.T) {
	t.Parallel()

	calendar, err := Load("cn_stock")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(embeddedTradingDays)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := calendar.Manifest().SHA256; got != want {
		t.Fatalf("manifest SHA256 = %q, want %q", got, want)
	}
}

func TestLoadRejectsUnknownCalendar(t *testing.T) {
	t.Parallel()

	if _, err := Load("unknown"); !errors.Is(err, ErrUnknownCalendar) {
		t.Fatalf("Load() error = %v, want ErrUnknownCalendar", err)
	}
}

func TestStatusDistinguishesTradingNonTradingAndOutOfCoverage(t *testing.T) {
	t.Parallel()

	calendar := mustLoad(t)
	tests := []struct {
		name   string
		date   string
		status CoverageStatus
	}{
		{name: "known trading day", date: "1992-05-04", status: TradingDay},
		{name: "known holiday", date: "2024-10-01", status: NonTradingDay},
		{name: "saturday", date: "2024-10-05", status: NonTradingDay},
		{name: "valid through", date: "2026-12-31", status: TradingDay},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			date := mustDate(t, tt.date)
			status, err := calendar.Status(date)
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if status != tt.status {
				t.Fatalf("Status() = %v, want %v", status, tt.status)
			}
		})
	}

	for _, value := range []string{"1990-12-18", "2027-01-01"} {
		date := mustDate(t, value)
		status, err := calendar.Status(date)
		if status != OutOfCoverage || !errors.Is(err, ErrOutOfCoverage) {
			t.Fatalf("Status(%s) = %v, %v; want OutOfCoverage and ErrOutOfCoverage", value, status, err)
		}
	}
}

func TestPreviousAndNextTradingDayAreStrictAndBounded(t *testing.T) {
	t.Parallel()

	calendar := mustLoad(t)
	date := mustDate(t, "1992-05-04")
	previous, err := calendar.PrevTradingDay(date)
	if err != nil || previous.String() != "1992-04-30" {
		t.Fatalf("PrevTradingDay() = %s, %v; want 1992-04-30", previous, err)
	}
	next, err := calendar.NextTradingDay(date)
	if err != nil || next.String() != "1992-05-05" {
		t.Fatalf("NextTradingDay() = %s, %v; want 1992-05-05", next, err)
	}
	nonTradingDate := mustDate(t, "2024-10-05")
	previous, err = calendar.PrevTradingDay(nonTradingDate)
	if err != nil || previous.String() != "2024-09-30" {
		t.Fatalf("PrevTradingDay(non-trading day) = %s, %v; want 2024-09-30", previous, err)
	}
	next, err = calendar.NextTradingDay(nonTradingDate)
	if err != nil || next.String() != "2024-10-08" {
		t.Fatalf("NextTradingDay(non-trading day) = %s, %v; want 2024-10-08", next, err)
	}

	first, last := calendar.FirstDate(), calendar.LastDate()
	if _, err := calendar.PrevTradingDay(first); !errors.Is(err, ErrNoPreviousTradingDay) {
		t.Fatalf("PrevTradingDay(first) error = %v, want ErrNoPreviousTradingDay", err)
	}
	if _, err := calendar.NextTradingDay(last); !errors.Is(err, ErrNoNextTradingDay) {
		t.Fatalf("NextTradingDay(last) error = %v, want ErrNoNextTradingDay", err)
	}
	if _, err := calendar.PrevTradingDay(mustDate(t, "1990-12-18")); !errors.Is(err, ErrOutOfCoverage) {
		t.Fatalf("PrevTradingDay(before first) error = %v, want ErrOutOfCoverage", err)
	}
	if _, err := calendar.NextTradingDay(mustDate(t, "2027-01-01")); !errors.Is(err, ErrOutOfCoverage) {
		t.Fatalf("NextTradingDay(after last) error = %v, want ErrOutOfCoverage", err)
	}
}

func TestTradingDaysIsInclusiveAndReturnsIndependentSlice(t *testing.T) {
	t.Parallel()

	calendar := mustLoad(t)
	start := mustDate(t, "1992-04-30")
	end := mustDate(t, "1992-05-05")
	dates, err := calendar.TradingDays(start, end)
	if err != nil {
		t.Fatalf("TradingDays() error = %v", err)
	}
	want := []string{"1992-04-30", "1992-05-04", "1992-05-05"}
	if len(dates) != len(want) {
		t.Fatalf("TradingDays() len = %d, want %d", len(dates), len(want))
	}
	for i, value := range want {
		if dates[i].String() != value {
			t.Fatalf("TradingDays()[%d] = %s, want %s", i, dates[i], value)
		}
	}

	dates[0] = mustDate(t, "2000-01-03")
	again, err := calendar.TradingDays(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].String() != "1992-04-30" {
		t.Fatalf("calendar data changed after result mutation: %s", again[0])
	}
}

func TestTradingDaysRejectsInvalidRangesAndOutOfCoverage(t *testing.T) {
	t.Parallel()

	calendar := mustLoad(t)
	tests := []struct {
		name      string
		start     string
		end       string
		wantError error
	}{
		{name: "reversed", start: "2024-01-02", end: "2024-01-01", wantError: ErrInvalidRange},
		{name: "start before coverage", start: "1990-12-18", end: "1990-12-19", wantError: ErrOutOfCoverage},
		{name: "end after coverage", start: "2026-12-31", end: "2027-01-01", wantError: ErrOutOfCoverage},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := calendar.TradingDays(mustDate(t, tt.start), mustDate(t, tt.end))
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("TradingDays() error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestParseCalendarDataRejectsEmptyInvalidDuplicateAndUnsortedDates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: `{"calendar_id":"cn_stock","trading_days":[]}`},
		{name: "empty date", raw: `{"calendar_id":"cn_stock","trading_days":[""]}`},
		{name: "invalid date", raw: `{"calendar_id":"cn_stock","trading_days":["2024-02-30"]}`},
		{name: "duplicate date", raw: `{"calendar_id":"cn_stock","trading_days":["2024-01-02","2024-01-02"]}`},
		{name: "not strictly ascending", raw: `{"calendar_id":"cn_stock","trading_days":["2024-01-03","2024-01-02"]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseCalendarData([]byte(tt.raw)); err == nil {
				t.Fatal("parseCalendarData() accepted invalid data")
			}
		})
	}
}

func TestLoadCalendarValidatesManifestHashAndCoverage(t *testing.T) {
	t.Parallel()

	data := []byte(`{"calendar_id":"cn_stock","trading_days":["2024-01-02","2024-01-03"]}`)
	sum := sha256.Sum256(data)
	manifest := fmt.Sprintf(`{"calendar_id":"cn_stock","source":"test","version":1,"valid_from":"2024-01-02","valid_through":"2024-01-03","sha256":"sha256:%s"}`, hex.EncodeToString(sum[:]))
	if _, err := loadCalendar(data, []byte(manifest)); err != nil {
		t.Fatalf("loadCalendar() error = %v", err)
	}

	for _, mutate := range []func(*calendarManifest){
		func(m *calendarManifest) { m.SHA256 = "sha256:" + strings.Repeat("0", sha256.Size*2) },
		func(m *calendarManifest) { m.ValidFrom = "2024-01-01" },
		func(m *calendarManifest) { m.ValidThrough = "2024-01-04" },
	} {
		mutated := calendarManifest{
			CalendarID:   "cn_stock",
			Source:       "test",
			Version:      1,
			ValidFrom:    "2024-01-02",
			ValidThrough: "2024-01-03",
			SHA256:       "sha256:" + hex.EncodeToString(sum[:]),
		}
		mutate(&mutated)
		badManifest := fmt.Sprintf(`{"calendar_id":%q,"source":%q,"version":%d,"valid_from":%q,"valid_through":%q,"sha256":%q}`,
			mutated.CalendarID, mutated.Source, mutated.Version, mutated.ValidFrom, mutated.ValidThrough, mutated.SHA256)
		if _, err := loadCalendar(data, []byte(badManifest)); err == nil {
			t.Fatal("loadCalendar() accepted invalid manifest")
		}
	}
}

func TestCoverageStatusString(t *testing.T) {
	t.Parallel()

	tests := map[CoverageStatus]string{
		TradingDay:    "trading_day",
		NonTradingDay: "non_trading_day",
		OutOfCoverage: "out_of_coverage",
	}
	for status, want := range tests {
		if got := status.String(); got != want {
			t.Errorf("CoverageStatus(%d).String() = %q, want %q", status, got, want)
		}
	}
}

func mustLoad(t *testing.T) TradingCalendar {
	t.Helper()
	calendar, err := Load("cn_stock")
	if err != nil {
		t.Fatal(err)
	}
	return calendar
}

func mustDate(t *testing.T, value string) CivilDate {
	t.Helper()
	date, err := ParseCivilDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return date
}
