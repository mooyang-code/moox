package eventconsumer

import (
	"testing"
	"time"
)

func TestPeriodTimeUsesUnixSecondsContract(t *testing.T) {
	want := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if got := periodTime(want.Unix()); !got.Equal(want) {
		t.Fatalf("period time = %s, want %s", got, want)
	}
}
