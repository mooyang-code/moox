package subjectsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeTagStore struct {
	tags     []*pb.Tag
	applied  map[string]int
	failures map[string]string
}

func (f *fakeTagStore) ListTags(context.Context) ([]*pb.Tag, error) { return f.tags, nil }
func (f *fakeTagStore) ApplyTagSnapshot(_ context.Context, _, tagID string, _ time.Time, items []*pb.TagSnapshotItem) error {
	f.applied[tagID] = len(items)
	return nil
}
func (f *fakeTagStore) ReportTagRunFailure(_ context.Context, _, tagID string, _ time.Time, message string) error {
	f.failures[tagID] = message
	return nil
}

func TestTagDue(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		tag  *pb.Tag
		want bool
	}{
		{"never run", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", UpdatedAt: "2026-09-25 09:00:00"}, true},
		{"cron elapsed", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", UpdatedAt: "2026-09-01 00:00:00"}, true},
		{"cron not elapsed", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 10:00:00", UpdatedAt: "2026-09-01 00:00:00"}, false},
		{"edited after run", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 10:00:00", UpdatedAt: "2026-09-25 10:10:00"}, true},
	}
	for _, c := range cases {
		got, err := tagDue(c.tag, now)
		if err != nil || got != c.want {
			t.Errorf("%s: due = %v, err = %v", c.name, got, err)
		}
	}
}

func TestRunOnceAppliesAndReports(t *testing.T) {
	store := &fakeTagStore{
		tags: []*pb.Tag{
			{SpaceId: "crypto", TagId: "binance_spot", Mode: "auto", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "binance_swap", Mode: "auto", Sources: []string{"binance"}, InstrumentType: "swap", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "watch", Mode: "manual", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "manual_probe", Mode: "manual", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "0 * * * *", Timezone: "UTC"},
		},
		applied: map[string]int{}, failures: map[string]string{},
	}
	listers := Listers{
		{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{Source: "binance", InstrumentType: "swap"}: fakeSubjectLister{err: errors.New("451")},
	}
	runner := &TagRunner{Store: store, Listers: listers, FetchTimeout: time.Second, Now: func() time.Time { return time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC) }}
	runner.RunOnce(context.Background())
	if store.applied["binance_spot"] != 1 {
		t.Fatalf("applied = %v", store.applied)
	}
	if store.failures["binance_swap"] == "" {
		t.Fatalf("failures = %v", store.failures)
	}
	if _, ok := store.applied["watch"]; ok {
		t.Fatal("manual tag without probe must be skipped")
	}
	if store.applied["manual_probe"] != 1 {
		t.Fatalf("manual probe should apply a validity snapshot: %v", store.applied)
	}
}
