package subjectsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/cnstock"
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
		{MarketID: "crypto", Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{MarketID: "crypto", Source: "binance", InstrumentType: "swap"}: fakeSubjectLister{err: errors.New("451")},
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
		{MarketID: "crypto", Source: "binance", InstrumentType: "swap"}: flakyLister{calls: &calls, failures: 2, items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{MarketID: "crypto", Source: "binance", InstrumentType: "spot"}: fakeSubjectLister{err: errors.New("must not be called")},
	}}
	results, err := runner.SyncTags(context.Background(), Filter{SpaceID: "crypto", MarketType: "swap"})
	if err != nil || len(results) != 1 || results[0].Status != StatusSuccess || calls != 3 || store.applied["binance_swap"] != 1 {
		t.Fatalf("results=%v err=%v calls=%d applied=%v", results, err, calls, store.applied)
	}
	calls = 0
	store.applied = map[string]int{}
	runner.Listers[ListerKey{MarketID: "crypto", Source: "binance", InstrumentType: "swap"}] = flakyLister{calls: &calls, failures: 3}
	results, _ = runner.SyncTags(context.Background(), Filter{SpaceID: "crypto", MarketType: "swap"})
	if results[0].Status != StatusFailed || calls != 3 || len(store.applied) != 0 || runs.runs["binance_swap"].LastStatus != "failed" {
		t.Fatalf("a failed snapshot must keep members: results=%v calls=%d applied=%v", results, calls, store.applied)
	}
}

func TestSyncTagsUsesIndexListersForIndexTags(t *testing.T) {
	hs300 := autoTag("stockcn", "cn_hs300", "auto", "eastmoney", "equity")
	hs300.Attributes["sync.index"] = "000300"
	store := &fakeTagStore{tags: []*pb.Tag{hs300, autoTag("stockcn", "cn_a_share", "auto", "eastmoney", "equity")}, applied: map[string]int{}}
	runs := newFakeRunStore()
	calls := 0
	runner := &TagRunner{
		Store: store, Runs: runs, FetchTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond, time.Millisecond},
		Listers:      Listers{{MarketID: "stockcn", Source: "eastmoney", InstrumentType: "equity"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG"}, {SubjectID: "600519.XSHG"}, {SubjectID: "000001.XSHE"}}}},
		IndexListers: IndexListers{{MarketID: "stockcn", Source: "eastmoney", Index: "000300"}: flakyLister{calls: &calls, failures: 1, items: []marketdata.Instrument{{SubjectID: "600519.XSHG"}, {SubjectID: "000001.XSHE"}}}},
	}
	results, err := runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn"})
	if err != nil || len(results) != 2 || results[0].Status != StatusSuccess || results[1].Status != StatusSuccess {
		t.Fatalf("results=%v err=%v", results, err)
	}
	if store.applied["cn_hs300"] != 2 || store.applied["cn_a_share"] != 3 || calls != 2 {
		t.Fatalf("带 sync.index 的标签取成分股，其余取全部标的：applied=%v calls=%d", store.applied, calls)
	}

	// 成分股取数一直失败：不应用快照，保留原成员，记录失败。
	calls = 0
	store.applied = map[string]int{}
	runner.IndexListers[IndexListerKey{MarketID: "stockcn", Source: "eastmoney", Index: "000300"}] = flakyLister{calls: &calls, failures: 3}
	results, _ = runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn", TagID: "cn_hs300"})
	if len(results) != 1 || results[0].Status != StatusFailed || calls != 3 || len(store.applied) != 0 || runs.runs["cn_hs300"].LastStatus != "failed" {
		t.Fatalf("a failed constituent snapshot must keep members: results=%v calls=%d applied=%v", results, calls, store.applied)
	}

	// 没有登记的指数直接报错。
	hs300.Attributes["sync.index"] = "399001"
	results, _ = runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn", TagID: "cn_hs300"})
	if results[0].Status != StatusFailed {
		t.Fatalf("unknown index must fail: %v", results)
	}
}

// 指数成分来源：不写 sync.index_source 时按 sync.source 查成分表（cn_hs300 的现有行为）；
// 写了就按它查，sync.source 仍是采集用的行情源，不参与查成分表；全部标的那条路径只看 sync.source。
func TestSyncTagsReadsIndexSourceForIndexTags(t *testing.T) {
	hs300 := autoTag("stockcn", "cn_hs300", "auto", "eastmoney", "equity")
	hs300.Attributes["sync.index"] = "000300"
	other := autoTag("stockcn", "other_index", "auto", "eastmoney", "equity")
	other.Attributes["sync.index"] = "HSI"
	other.Attributes["sync.index_source"] = " Tencent "
	// 没有 sync.index 的标签写了 sync.index_source 也不起作用。
	all := autoTag("stockcn", "cn_a_share", "auto", "eastmoney", "equity")
	all.Attributes["sync.index_source"] = "tencent"
	store := &fakeTagStore{tags: []*pb.Tag{hs300, other, all}, applied: map[string]int{}}
	runner := &TagRunner{
		Store: store, FetchTimeout: time.Second, RetryDelays: []time.Duration{},
		Listers: Listers{{MarketID: "stockcn", Source: "eastmoney", InstrumentType: "equity"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG"}, {SubjectID: "600519.XSHG"}, {SubjectID: "000001.XSHE"}, {SubjectID: "000002.XSHE"}}}},
		IndexListers: IndexListers{
			{MarketID: "stockcn", Source: "eastmoney", Index: "000300"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600519.XSHG"}, {SubjectID: "000001.XSHE"}}},
			{MarketID: "stockcn", Source: "tencent", Index: "HSI"}:      fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG"}}},
		},
	}
	results, err := runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn"})
	if err != nil || len(results) != 3 {
		t.Fatalf("results=%v err=%v", results, err)
	}
	for _, result := range results {
		if result.Status != StatusSuccess {
			t.Fatalf("results=%v", results)
		}
	}
	if store.applied["cn_hs300"] != 2 || store.applied["other_index"] != 1 || store.applied["cn_a_share"] != 4 {
		t.Fatalf("applied=%v", store.applied)
	}

	// 成分来源没有登记这个指数：报错里写的是成分来源，不回落到 sync.source。
	other.Attributes["sync.index_source"] = "sina"
	results, _ = runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn", TagID: "other_index"})
	if results[0].Status != StatusFailed || !strings.Contains(results[0].Message, "sina") {
		t.Fatalf("unknown index source must fail: %v", results)
	}
}

// 东方财富主机冷却中：请求根本没发出去，同一次同步里不再整次重跑（全部标的与指数成分两条路径都一样）；
// 其他错误照旧重试。错误经过各层 %w 包装后仍要能识别。
func TestSyncTagsDoesNotRetryCoolingDown(t *testing.T) {
	cooling := fmt.Errorf("对照交易所：读东方财富美股列表 m:105：%w", &cnstock.CoolingDownError{Host: "push2.eastmoney.com", Remaining: 9 * time.Minute})
	calls := 0
	failing := countingLister{calls: &calls, err: cooling}
	hs300 := autoTag("stockcn", "cn_hs300", "auto", "eastmoney", "equity")
	hs300.Attributes["sync.index"] = "000300"
	store := &fakeTagStore{tags: []*pb.Tag{hs300, autoTag("stockcn", "cn_a_share", "auto", "eastmoney", "equity")}, applied: map[string]int{}}
	runs := newFakeRunStore()
	runner := &TagRunner{
		Store: store, Runs: runs, FetchTimeout: time.Second, RetryDelays: []time.Duration{time.Millisecond, time.Millisecond},
		Listers:      Listers{{MarketID: "stockcn", Source: "eastmoney", InstrumentType: "equity"}: failing},
		IndexListers: IndexListers{{MarketID: "stockcn", Source: "eastmoney", Index: "000300"}: failing},
	}
	results, err := runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn"})
	if err != nil || len(results) != 2 {
		t.Fatalf("results=%v err=%v", results, err)
	}
	for _, result := range results {
		if result.Status != StatusFailed || !strings.Contains(result.Message, "冷却") {
			t.Fatalf("冷却应记为失败并带上原因：%+v", result)
		}
	}
	if calls != 2 {
		t.Fatalf("两个标签各取一次，不应整次重跑：%d 次", calls)
	}
	if len(store.applied) != 0 || runs.runs["cn_a_share"].LastStatus != "failed" {
		t.Fatalf("失败不应改成员：applied=%v", store.applied)
	}

	// 其他错误仍按原来的重试：3 次。
	calls = 0
	failing.err = errors.New("暂时的网络错误")
	runner.Listers[ListerKey{MarketID: "stockcn", Source: "eastmoney", InstrumentType: "equity"}] = failing
	if _, err := runner.SyncTags(context.Background(), Filter{SpaceID: "stockcn", TagID: "cn_a_share"}); err != nil || calls != 3 {
		t.Fatalf("其他错误应重试到 3 次：calls=%d err=%v", calls, err)
	}
}

type countingLister struct {
	calls *int
	err   error
}

func (l countingLister) List(context.Context) ([]marketdata.Instrument, error) {
	*l.calls++
	return nil, l.err
}
