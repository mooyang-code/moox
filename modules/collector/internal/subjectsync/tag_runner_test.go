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
			{SpaceId: "crypto", TagId: "binance_spot", Mode: "auto", Source: "binance", MarketType: "spot", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "binance_swap", Mode: "auto", Source: "binance", MarketType: "swap", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "watch", Mode: "manual", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "manual_bound", Mode: "manual", Source: "binance", MarketType: "spot", Cron: "0 * * * *", Timezone: "UTC"},
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
	if _, ok := store.applied["manual_bound"]; ok {
		t.Fatalf("manual tag membership must remain operator-owned: %v", store.applied)
	}
}

type flakyLister struct {
	calls    *int
	failures int
	items    []marketdata.Instrument
}

func (l flakyLister) List(context.Context) ([]marketdata.Instrument, error) {
	*l.calls++
	if *l.calls <= l.failures {
		return nil, errors.New("exchange returned an incomplete snapshot")
	}
	return l.items, nil
}

// 快照取数失败或校验不通过时在本次同步内退避重试；都失败就保持原成员不变；只同步指定市场的标签。
func TestSyncTagsRetriesSnapshotsAndKeepsMembersOnFailure(t *testing.T) {
	store := &fakeTagStore{
		tags: []*pb.Tag{
			{SpaceId: "crypto", TagId: "binance_spot", Mode: "auto", Source: "binance", MarketType: "spot"},
			{SpaceId: "crypto", TagId: "binance_swap", Mode: "auto", Source: "binance", MarketType: "swap"},
		},
		applied: map[string]int{}, failures: map[string]string{},
	}
	calls := 0
	runner := &TagRunner{Store: store, FetchTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond, time.Millisecond}, Listers: Listers{
		{Source: "binance", InstrumentType: "swap"}: flakyLister{calls: &calls, failures: 2, items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{err: errors.New("must not be called")},
	}}
	results, err := runner.SyncTags(context.Background(), Filter{SpaceID: "crypto", MarketType: "swap"})
	if err != nil || len(results) != 1 || results[0].Status != StatusSuccess || calls != 3 || store.applied["binance_swap"] != 1 {
		t.Fatalf("results=%v err=%v calls=%d applied=%v", results, err, calls, store.applied)
	}
	calls = 0
	store.applied = map[string]int{}
	runner.Listers[ListerKey{Source: "binance", InstrumentType: "swap"}] = flakyLister{calls: &calls, failures: 3}
	results, _ = runner.SyncTags(context.Background(), Filter{SpaceID: "crypto", MarketType: "swap"})
	if results[0].Status != StatusFailed || calls != 3 || len(store.applied) != 0 || store.failures["binance_swap"] == "" {
		t.Fatalf("a failed snapshot must keep members: results=%v calls=%d applied=%v", results, calls, store.applied)
	}
}
