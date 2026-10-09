package subjectsync

import (
	"context"
	"errors"
	"strings"
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

func TestRunDueAppliesAndReports(t *testing.T) {
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
	runner := &TagRunner{Store: store, Listers: listers, FetchTimeout: time.Second}
	err := runner.RunDue(context.Background(), time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "binance_swap") {
		t.Fatalf("失败的标签应当体现在返回的错误里: %v", err)
	}
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

func TestRunDueSyncsOnlyDueTags(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 30, 30, 0, time.UTC)
	store := &fakeTagStore{
		tags: []*pb.Tag{
			// 每小时一次，上次 10:00 已跑过：未到期。
			{SpaceId: "crypto", TagId: "fresh", Mode: "auto", Source: "binance", MarketType: "spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-10-09 10:00:00", UpdatedAt: "2026-10-01 00:00:00"},
			// 上次 09:00：10:00 的计划已过，到期。
			{SpaceId: "crypto", TagId: "stale", Mode: "auto", Source: "binance", MarketType: "spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-10-09 09:00:00", UpdatedAt: "2026-10-01 00:00:00"},
			// 从未运行：到期。
			{SpaceId: "crypto", TagId: "new", Mode: "auto", Source: "binance", MarketType: "swap", Cron: "0 * * * *", Timezone: "UTC"},
		},
		applied: map[string]int{}, failures: map[string]string{},
	}
	listers := Listers{
		{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{Source: "binance", InstrumentType: "swap"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "ETH-USDT"}}},
	}
	if err := (&TagRunner{Store: store, Listers: listers}).RunDue(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.applied["fresh"]; ok {
		t.Fatalf("未到期的标签不应同步: %v", store.applied)
	}
	if store.applied["stale"] != 1 || store.applied["new"] != 1 {
		t.Fatalf("到期的标签应当同步: %v", store.applied)
	}
}

type failingListTagStore struct{ fakeTagStore }

func (*failingListTagStore) ListTags(context.Context) ([]*pb.Tag, error) {
	return nil, errors.New("storage unavailable")
}

func TestRunDueReturnsListFailure(t *testing.T) {
	err := (&TagRunner{Store: &failingListTagStore{}}).RunDue(context.Background(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "读取标签失败") {
		t.Fatalf("读取标签失败应当返回错误: %v", err)
	}
}
