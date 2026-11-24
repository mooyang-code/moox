package report

import (
	"testing"
	"time"
)

func TestEvaluateObjectStore(t *testing.T) {
	now := time.Date(2026, 11, 7, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		since time.Time
		want  int
	}{
		{time.Time{}, ObjectStoreAvailable},
		{now.Add(-4 * time.Minute), ObjectStoreUnavailable},
		{now.Add(-5 * time.Minute), ObjectStoreDown},
	}
	for _, c := range cases {
		if got := EvaluateObjectStore(c.since, now); got != c.want {
			t.Fatalf("EvaluateObjectStore(%v) = %d, want %d", c.since, got, c.want)
		}
	}
}
