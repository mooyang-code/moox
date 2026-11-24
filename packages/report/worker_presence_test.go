package report

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluatePresence(t *testing.T) {
	now := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		required bool
		last     time.Time
		want     int
	}{
		{false, time.Time{}, PresenceNotRequired},
		{true, now.Add(-90 * time.Second), PresenceOnline},
		{true, now.Add(-121 * time.Second), PresenceOffline},
		{true, time.Time{}, PresenceOffline},
	}
	for _, c := range cases {
		if got := EvaluatePresence(c.required, c.last, now, 0); got != c.want {
			t.Fatalf("EvaluatePresence(%v, %v) = %d, want %d", c.required, c.last, got, c.want)
		}
	}
}

// 离线才报，detail 写机器与最后心跳；从没心跳过的单独说明。
func TestPresenceAttention(t *testing.T) {
	last := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	if got := PresenceAttention(PresenceOnline, "factor-engine@m1", last); got != nil {
		t.Fatalf("在线不应上报: %v", got)
	}
	if got := PresenceAttention(PresenceNotRequired, "", time.Time{}); got != nil {
		t.Fatalf("不需要时不应上报: %v", got)
	}
	got := PresenceAttention(PresenceOffline, "factor-engine@m1", last)
	if len(got) != 1 || !strings.Contains(got[0], "factor-engine@m1 最后心跳 10-18 06:00 UTC") {
		t.Fatalf("离线 detail 不对: %v", got)
	}
	if got := PresenceAttention(PresenceOffline, "", time.Time{}); len(got) != 1 || !strings.Contains(got[0], "从未收到心跳") {
		t.Fatalf("从未心跳 detail 不对: %v", got)
	}
}
