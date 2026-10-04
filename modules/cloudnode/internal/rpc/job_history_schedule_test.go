package rpc

import (
	"context"
	"testing"
	"time"
)

type fakeHistoryMaintainer struct {
	called bool
	now    time.Time
	err    error
}

func setDefaultJobHistoryMaintainerForTest(maintainer jobHistoryMaintainer, now func() time.Time) func() {
	defaultJobHistoryMaintenance.Lock()
	oldMaintainer := defaultJobHistoryMaintenance.maintainer
	oldNow := defaultJobHistoryMaintenance.now
	defaultJobHistoryMaintenance.maintainer = maintainer
	defaultJobHistoryMaintenance.now = now
	defaultJobHistoryMaintenance.Unlock()
	return func() {
		defaultJobHistoryMaintenance.Lock()
		defaultJobHistoryMaintenance.maintainer = oldMaintainer
		defaultJobHistoryMaintenance.now = oldNow
		defaultJobHistoryMaintenance.Unlock()
	}
}

func (f *fakeHistoryMaintainer) MaintainDaily(_ context.Context, now time.Time) error {
	f.called = true
	f.now = now
	return f.err
}

func TestHandleJobHistoryScheduleCallsConfiguredMaintainer(t *testing.T) {
	fake := &fakeHistoryMaintainer{}
	restore := setDefaultJobHistoryMaintainerForTest(fake, func() time.Time {
		return time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	})
	defer restore()

	if err := HandleJobHistorySchedule(context.Background(), ""); err != nil {
		t.Fatalf("HandleJobHistorySchedule() error = %v", err)
	}
	if !fake.called {
		t.Fatal("maintainer was not called")
	}
	if got := fake.now.Format("20060102"); got != "20260707" {
		t.Fatalf("now day = %s, want 20260707", got)
	}
}

func TestHandleJobHistorySchedulePreservesTriggerTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	fake := &fakeHistoryMaintainer{}
	restore := setDefaultJobHistoryMaintainerForTest(fake, func() time.Time {
		return time.Date(2026, 7, 7, 0, 5, 0, 0, loc)
	})
	defer restore()

	if err := HandleJobHistorySchedule(context.Background(), ""); err != nil {
		t.Fatalf("HandleJobHistorySchedule() error = %v", err)
	}
	if got := fake.now.Location().String(); got != "Asia/Shanghai" {
		t.Fatalf("location = %s, want Asia/Shanghai", got)
	}
	if got := fake.now.Format("20060102"); got != "20260707" {
		t.Fatalf("now day = %s, want 20260707", got)
	}
}

func TestHandleJobHistoryScheduleUsesShanghaiDayWhenProcessClockIsUTC(t *testing.T) {
	fake := &fakeHistoryMaintainer{}
	restore := setDefaultJobHistoryMaintainerForTest(fake, func() time.Time {
		return time.Date(2026, 7, 6, 16, 5, 0, 0, time.UTC)
	})
	defer restore()

	if err := HandleJobHistorySchedule(context.Background(), ""); err != nil {
		t.Fatalf("HandleJobHistorySchedule() error = %v", err)
	}
	if got := fake.now.Location().String(); got != "Asia/Shanghai" {
		t.Fatalf("location = %s, want Asia/Shanghai", got)
	}
	if got := fake.now.Format("20060102"); got != "20260707" {
		t.Fatalf("now day = %s, want 20260707", got)
	}
}
