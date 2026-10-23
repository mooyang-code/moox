package frequency

import (
	"testing"
	"time"
)

func TestParseAcceptsCanonicalValuesAndAliases(t *testing.T) {
	cases := map[string]Frequency{
		"1m": Minute1, " 5m ": Minute5, "1h": Hour1, "1H": Hour1, "60m": Hour1,
		"4H": Hour4, "240m": Hour4, "1D": Day1, "1W": Week1, "1M": Month1, "1mo": Month1, "30s": Second30,
		"45m": "45m", "2h": "2h", "120m": "2h", "1440m": Day1, "24h": Day1, "12H": "12h", "288m": "288m",
	}
	for raw, want := range cases {
		got, err := Parse(raw)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestParseRejectsUnknownFrequencies(t *testing.T) {
	// 7m / 50m / 5h 不能整除一天，桶边界会逐日漂移；45M 的大写 M 是月，不是分钟；60M 别名已删除。
	for _, raw := range []string{"", "7m", "50m", "5h", "05m", "0m", "-5m", "45M", "60M", "1y", "quarter", "1MIN", "2d", "1441m", "25h", "99999999999m", "m", "h", "1.5h"} {
		if got, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) = %q; want an error", raw, got)
		}
	}
}

func TestEveryDivisorOfOneDayIsAccepted(t *testing.T) {
	count := 0
	for minutes := 1; minutes < minutesPerDay; minutes++ {
		if minutesPerDay%minutes != 0 {
			continue
		}
		count++
		canonical := fromMinutes(minutes)
		if !IsCanonical(string(canonical)) || canonical.Duration() != time.Duration(minutes)*time.Minute {
			t.Errorf("%d 分钟的规范值 %q 不成立", minutes, canonical)
		}
	}
	if count != 35 {
		t.Fatalf("1440 的真因子应有 35 个，实际 %d", count)
	}
}

func TestIsCanonicalRejectsAliases(t *testing.T) {
	for _, raw := range []string{"1H", "1M", "60m", "1D", "120m", "1440m", "24h", "12H", "05m"} {
		if IsCanonical(raw) {
			t.Errorf("IsCanonical(%q) = true; aliases are not canonical", raw)
		}
	}
	for _, raw := range []string{"1m", "45m", "2h", "12h", "1d", "1w", "1mo", "30s"} {
		if !IsCanonical(raw) {
			t.Errorf("IsCanonical(%q) = false", raw)
		}
	}
	for _, frequency := range Standard() {
		if !IsCanonical(string(frequency)) {
			t.Errorf("standard frequency %q is not canonical", frequency)
		}
	}
}

func TestDurationAndBarEnd(t *testing.T) {
	if got := Hour4.Duration(); got != 4*time.Hour {
		t.Fatalf("4h duration = %v", got)
	}
	if got := Frequency("45m").Duration(); got != 45*time.Minute {
		t.Fatalf("45m duration = %v", got)
	}
	if got := Frequency("12h").Duration(); got != 12*time.Hour {
		t.Fatalf("12h duration = %v", got)
	}
	if got := Month1.Duration(); got != 0 {
		t.Fatalf("1mo duration = %v; a month has no fixed length", got)
	}
	// 非规范写法不报时长，避免绕过规范化。
	if got := Frequency("60m").Duration(); got != 0 {
		t.Fatalf("non-canonical 60m duration = %v", got)
	}
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if got := Month1.BarEnd(start); !got.Equal(start.AddDate(0, 1, 0)) {
		t.Fatalf("1mo bar end = %v", got)
	}
	if got := Minute15.BarEnd(start); !got.Equal(start.Add(15 * time.Minute)) {
		t.Fatalf("15m bar end = %v", got)
	}
	for _, frequency := range Standard() {
		if frequency != Month1 && frequency.Duration() <= 0 {
			t.Errorf("%s has no duration", frequency)
		}
		if frequency.NominalDuration() <= 0 {
			t.Errorf("%s has no nominal duration", frequency)
		}
	}
	if got := Month1.NominalDuration(); got != 30*24*time.Hour {
		t.Fatalf("1mo nominal duration = %v", got)
	}
}

func TestIsIntraday(t *testing.T) {
	for raw, want := range map[string]bool{"1m": true, "45m": true, "12h": true, "1d": false, "1w": false, "1mo": false, "30s": true} {
		if got := Frequency(raw).IsIntraday(); got != want {
			t.Errorf("%s.IsIntraday() = %v; want %v", raw, got, want)
		}
	}
}
