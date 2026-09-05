package trigger

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerRejectsInvalidCron(t *testing.T) {
	s := &Scheduler{}
	err := s.Start(context.Background(), []ScheduleJob{{Cron: "not-a-cron", Run: func(context.Context, time.Time) error { return nil }}})
	if err == nil {
		t.Fatal("invalid cron should be rejected")
	}
}

func TestSchedulerDynamicRefreshesJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var loads atomic.Int32
	var runs atomic.Int32
	s := &Scheduler{}
	err := s.StartDynamic(ctx, 5*time.Millisecond, func(context.Context) ([]ScheduleJob, error) {
		loads.Add(1)
		return []ScheduleJob{{Cron: "* * * * *", Run: func(context.Context, time.Time) error { runs.Add(1); return nil }}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	time.Sleep(20 * time.Millisecond)
	if loads.Load() < 1 {
		t.Fatal("dynamic scheduler did not load jobs")
	}
}

func TestCronSpecUsesOccurrenceThatJustFired(t *testing.T) {
	spec := &cronSpecWithLocation{Schedule: &fixedSchedule{times: []time.Time{
		time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC),
	}}, Location: time.UTC}
	first := spec.Next(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if !first.Equal(time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("first occurrence = %v", first)
	}
	second := spec.Next(first)
	if !second.Equal(time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("second occurrence = %v", second)
	}
	if got := spec.PreviousTime(); !got.Equal(first) {
		t.Fatalf("previous occurrence = %v, want %v", got, first)
	}
}

type fixedSchedule struct{ times []time.Time }

func (s *fixedSchedule) Next(time.Time) time.Time {
	if len(s.times) == 0 {
		return time.Time{}
	}
	next := s.times[0]
	s.times = s.times[1:]
	return next
}
