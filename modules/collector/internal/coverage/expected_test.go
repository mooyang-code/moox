package coverage

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

func TestExpectedBucketsHonorLunchBreakAndCalendarSessions(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	date := time.Date(2026, 7, 13, 0, 0, 0, 0, location)
	sessions := []Session{{TradeDate: "2026-07-13", Open: date.Add(9*time.Hour + 30*time.Minute), Close: date.Add(11*time.Hour + 30*time.Minute), DailyAnchor: date}, {TradeDate: "2026-07-13", Open: date.Add(13 * time.Hour), Close: date.Add(15 * time.Hour), DailyAnchor: date}}
	buckets, err := ExpectedBuckets(date.UTC(), date.Add(24*time.Hour).UTC(), marketdata.FrequencyMinute, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 240 {
		t.Fatalf("minute buckets=%d, want 240", len(buckets))
	}
	for _, bucket := range buckets {
		local := bucket.In(location).Format("15:04")
		if local >= "11:30" && local < "13:00" {
			t.Fatalf("lunch bucket generated: %s", local)
		}
	}
	daily, err := ExpectedBuckets(date.UTC(), date.Add(24*time.Hour).UTC(), marketdata.FrequencyDay, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || !daily[0].Equal(date.UTC()) {
		t.Fatalf("daily=%v", daily)
	}
}

func TestMissingRangesCompactByExpectedSequenceAcrossSessionBreak(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	expected := []time.Time{base, base.Add(time.Minute), base.Add(2 * time.Minute), base.Add(90 * time.Minute), base.Add(91 * time.Minute)}
	ranges := MissingRanges(expected, []time.Time{base, base.Add(91 * time.Minute)})
	if len(ranges) != 1 || ranges[0].Buckets != 3 || !ranges[0].End.Equal(base.Add(90*time.Minute)) {
		t.Fatalf("ranges=%+v", ranges)
	}
}
