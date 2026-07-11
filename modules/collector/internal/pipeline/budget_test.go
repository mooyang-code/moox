package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestBudgetStopsBeforeReservedReportWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	b := NewBudget(context.Background(), now, 30*time.Second, 10*time.Second)
	if !b.CanStart(now.Add(19*time.Second), time.Second) {
		t.Fatal("work before reserve should be allowed")
	}
	if b.CanStart(now.Add(20*time.Second), time.Second) {
		t.Fatal("work that crosses report reserve should be denied")
	}
}
