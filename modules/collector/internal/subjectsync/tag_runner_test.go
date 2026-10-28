package subjectsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeTagStore struct {
	tags    []*pb.Tag
	applied map[string]int
}

func (f *fakeTagStore) ListTags(context.Context) ([]*pb.Tag, error) { return f.tags, nil }
func (f *fakeTagStore) ApplyTagSnapshot(_ context.Context, _, tagID string, items []*pb.TagSnapshotItem) error {
	f.applied[tagID] = len(items)
	return nil
}

// fakeRunStore 在内存里保存标签的同步运行结果。
type fakeRunStore struct{ runs map[string]store.TagSyncRun }

func newFakeRunStore() *fakeRunStore { return &fakeRunStore{runs: map[string]store.TagSyncRun{}} }

func (f *fakeRunStore) List(context.Context, string) ([]store.TagSyncRun, error) {
	out := make([]store.TagSyncRun, 0, len(f.runs))
	for _, run := range f.runs {
		out = append(out, run)
	}
	return out, nil
}

func (f *fakeRunStore) Record(_ context.Context, run store.TagSyncRun) error {
	f.runs[run.TagID] = run
	return nil
}

// autoTag 构造带同步配置属性的标签。
func autoTag(spaceID, tagID, mode, source, marketType string) *pb.Tag {
	attributes := map[string]string{}
	if source != "" {
		attributes["sync.source"] = source
	}
	if marketType != "" {
		attributes["market_type"] = marketType
	}
	return &pb.Tag{SpaceId: spaceID, TagId: tagID, Mode: mode, Attributes: attributes}
}

func TestTagDue(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	cron := map[string]string{"sync.cron": "0 * * * *", "sync.timezone": "UTC"}
	run := func(at string) *store.TagSyncRun {
		parsed, err := time.Parse("2006-01-02 15:04:05", at)
		if err != nil {
			t.Fatal(err)
		}
		return &store.TagSyncRun{LastRunAt: parsed.UTC()}
	}
	cases := []struct {
		name string
		tag  *pb.Tag
		run  *store.TagSyncRun
		want bool
	}{
		{"never run", &pb.Tag{Attributes: cron, UpdatedAt: "2026-09-25 09:00:00"}, nil, true},
		{"cron elapsed", &pb.Tag{Attributes: cron, UpdatedAt: "2026-09-01 00:00:00"}, run("2026-09-25 09:00:00"), true},
		{"cron not elapsed", &pb.Tag{Attributes: cron, UpdatedAt: "2026-09-01 00:00:00"}, run("2026-09-25 10:00:00"), false},
		{"edited after run", &pb.Tag{Attributes: cron, UpdatedAt: "2026-09-25 10:10:00"}, run("2026-09-25 10:00:00"), true},
		{"default cron when unset", &pb.Tag{UpdatedAt: "2026-09-01 00:00:00"}, run("2026-09-25 09:00:00"), true},
	}
	for _, c := range cases {
		got, err := tagDue(c.tag, c.run, now)
		if err != nil || got != c.want {
			t.Errorf("%s: due = %v, err = %v", c.name, got, err)
		}
	}
}

func TestRunOnceAppliesAndReports(t *testing.T) {
	store := &fakeTagStore{
		tags: []*pb.Tag{
			autoTag("crypto", "binance_spot", "auto", "binance", "spot"),
			autoTag("crypto", "binance_swap", "auto", "binance", "swap"),
			autoTag("crypto", "watch", "manual", "", ""),
			autoTag("crypto", "manual_bound", "manual", "binance", "spot"),
		},
		applied: map[string]int{},
	}
	runs := newFakeRunStore()
	listers := Listers{
		{Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{Source: "binance", InstrumentType: "swap"}: fakeSubjectLister{err: errors.New("451")},
	}
	runner := &TagRunner{Store: store, Runs: runs, Listers: listers, FetchTimeout: time.Second, Now: func() time.Time { return time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC) }}
	runner.RunOnce(context.Background())
	if store.applied["binance_spot"] != 1 {
		t.Fatalf("applied = %v", store.applied)
	}
	if run := runs.runs["binance_swap"]; run.LastStatus != "failed" || run.LastError == "" || !run.LastSuccessAt.IsZero() {
		t.Fatalf("失败应记录在运行表里: %+v", run)
	}
	if run := runs.runs["binance_spot"]; run.LastStatus != "success" || run.LastError != "" {
		t.Fatalf("成功应记录在运行表里: %+v", run)
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
			autoTag("crypto", "binance_spot", "auto", "binance", "spot"),
			autoTag("crypto", "binance_swap", "auto", "binance", "swap"),
		},
		applied: map[string]int{},
	}
	runs := newFakeRunStore()
	calls := 0
	runner := &TagRunner{Store: store, Runs: runs, FetchTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond, time.Millisecond}, Listers: Listers{
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
	if results[0].Status != StatusFailed || calls != 3 || len(store.applied) != 0 || runs.runs["binance_swap"].LastStatus != "failed" {
		t.Fatalf("a failed snapshot must keep members: results=%v calls=%d applied=%v", results, calls, store.applied)
	}
}
