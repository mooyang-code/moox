package frequency

import (
	"testing"
	"time"
)

func TestParseAcceptsCanonicalValuesAndAliases(t *testing.T) {
	cases := map[string]Frequency{
		"1m": Minute1, " 5m ": Minute5, "1h": Hour1, "1H": Hour1, "60m": Hour1,
		"4H": Hour4, "1D": Day1, "1W": Week1, "1M": Month1, "1mo": Month1, "30s": Second30,
	}
	for raw, want := range cases {
		got, err := Parse(raw)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestParseRejectsUnknownFrequencies(t *testing.T) {
	for _, raw := range []string{"", "2h", "7m", "1y", "quarter", "1MIN"} {
		if got, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) = %q; want an error", raw, got)
		}
	}
}

func TestIsCanonicalRejectsAliases(t *testing.T) {
	for _, raw := range []string{"1H", "1M", "60m", "1D"} {
		if IsCanonical(raw) {
			t.Errorf("IsCanonical(%q) = true; aliases are not canonical", raw)
		}
	}
	for _, frequency := range All() {
		if !IsCanonical(string(frequency)) {
			t.Errorf("IsCanonical(%q) = false", frequency)
		}
	}
}

func TestDurationAndBarEnd(t *testing.T) {
	if got := Hour4.Duration(); got != 4*time.Hour {
		t.Fatalf("4h duration = %v", got)
	}
	if got := Month1.Duration(); got != 0 {
		t.Fatalf("1mo duration = %v; a month has no fixed length", got)
	}
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if got := Month1.BarEnd(start); !got.Equal(start.AddDate(0, 1, 0)) {
		t.Fatalf("1mo bar end = %v", got)
	}
	if got := Minute15.BarEnd(start); !got.Equal(start.Add(15 * time.Minute)) {
		t.Fatalf("15m bar end = %v", got)
	}
	for _, frequency := range All() {
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
