package subjectsync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func newTestSyncer(t *testing.T) (*Syncer, *fakeTagStore, *fakeAttributeStore) {
	t.Helper()
	// binance_spot 刚在整点同步过，按 cron 还没到期；手动同步必须无视这一点。
	justRun := "2026-09-25 10:00:00"
	store := &fakeTagStore{
		tags: []*pb.Tag{
			{SpaceId: "crypto", TagId: "binance_spot", Mode: "auto", Source: "binance", MarketType: "spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: justRun, UpdatedAt: "2026-09-01 00:00:00"},
			{SpaceId: "crypto", TagId: "binance_swap", Mode: "auto", Source: "binance", MarketType: "swap", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: justRun, UpdatedAt: "2026-09-01 00:00:00"},
			{SpaceId: "stockcn", TagId: "cn_a_share", Mode: "auto", Source: "eastmoney", MarketType: "equity", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: justRun, UpdatedAt: "2026-09-01 00:00:00"},
			{SpaceId: "crypto", TagId: "watch", Mode: "manual", Cron: "0 * * * *", Timezone: "UTC"},
		},
		applied: map[string]int{}, failures: map[string]string{},
	}
	listers := Listers{
		{Source: "binance", InstrumentType: "spot"}:     fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}, {SubjectID: "ETH-USDT"}}},
		{Source: "binance", InstrumentType: "swap"}:     fakeSubjectLister{err: errors.New("HTTP status 302")},
		{Source: "eastmoney", InstrumentType: "equity"}: fakeSubjectLister{items: []marketdata.Instrument{{SubjectID: "600000.SH", Exchange: "SH"}}},
	}
	now := func() time.Time { return time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC) }
	attributes := &fakeAttributeStore{}
	return &Syncer{
		Tags:       &TagRunner{Store: store, Listers: listers, FetchTimeout: time.Second, Now: now},
		Attributes: &AttributeRunner{Store: attributes, Listers: listers, FetchTimeout: time.Second, Now: now, Jobs: []AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "0 3 * * *", Timezone: "UTC"}}},
	}, store, attributes
}

func TestSyncNowRunsTagsThatAreNotDue(t *testing.T) {
	syncer, store, _ := newTestSyncer(t)
	report, err := syncer.SyncNow(context.Background(), Filter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if store.applied["binance_spot"] != 2 || store.applied["cn_a_share"] != 1 {
		t.Fatalf("applied = %v, a manual sync must run tags that are not due", store.applied)
	}
	byTag := map[string]TagResult{}
	for _, result := range report.Tags {
		byTag[result.TagID] = result
	}
	if r := byTag["binance_spot"]; r.Status != StatusSuccess || r.Subjects != 2 {
		t.Fatalf("spot result = %+v", r)
	}
	if r := byTag["binance_swap"]; r.Status != StatusFailed || r.Message == "" || store.failures["binance_swap"] == "" {
		t.Fatalf("swap result = %+v failures=%v, want the failure reported and recorded", r, store.failures)
	}
	if r := byTag["watch"]; r.Status != StatusSkipped {
		t.Fatalf("manual tag result = %+v, want skipped", r)
	}
	if !report.Failed() {
		t.Fatal("a failed tag must make the report failed")
	}
	if len(report.Attributes) != 0 {
		t.Fatalf("attributes ran without being requested: %+v", report.Attributes)
	}
}

func TestSyncNowFilterSelectsSpaceAndTag(t *testing.T) {
	syncer, store, _ := newTestSyncer(t)
	report, err := syncer.SyncNow(context.Background(), Filter{SpaceID: "crypto", TagID: "binance_spot"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tags) != 1 || report.Tags[0].TagID != "binance_spot" || len(store.applied) != 1 {
		t.Fatalf("report = %+v applied = %v, want only crypto/binance_spot", report.Tags, store.applied)
	}
	report, err = syncer.SyncNow(context.Background(), Filter{SpaceID: "stockcn"}, false)
	if err != nil || len(report.Tags) != 1 || report.Tags[0].TagID != "cn_a_share" {
		t.Fatalf("space filter report = %+v err = %v", report.Tags, err)
	}
	report, err = syncer.SyncNow(context.Background(), Filter{SpaceID: "crypto", TagID: "missing"}, false)
	if err != nil || len(report.Tags) != 0 {
		t.Fatalf("unknown tag report = %+v err = %v, want no results", report.Tags, err)
	}
}

func TestSyncNowIncludesAttributesOnRequestIgnoringCron(t *testing.T) {
	syncer, _, attributes := newTestSyncer(t)
	// 当前 10:30，任务的 cron 是每天 03:00，定时任务此刻不会运行。
	report, err := syncer.SyncNow(context.Background(), Filter{SpaceID: "crypto"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if attributes.calls != 1 || len(report.Attributes) != 1 || report.Attributes[0].Status != StatusSuccess {
		t.Fatalf("attribute calls = %d report = %+v", attributes.calls, report.Attributes)
	}
	if err := syncer.Attributes.RunOnce(context.Background()); err != nil || attributes.calls != 1 {
		t.Fatalf("scheduled run must still respect cron: calls = %d err = %v", attributes.calls, err)
	}
}

type blockingTagStore struct {
	fakeTagStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingTagStore) ListTags(ctx context.Context) ([]*pb.Tag, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.fakeTagStore.ListTags(ctx)
}

func TestSyncNowRefusesWhileAnotherSyncRuns(t *testing.T) {
	store := &blockingTagStore{started: make(chan struct{}), release: make(chan struct{})}
	syncer := &Syncer{Tags: &TagRunner{Store: store}}
	done := make(chan error, 1)
	go func() { done <- syncer.RunDueTags(context.Background()) }()
	<-store.started
	if _, err := syncer.SyncNow(context.Background(), Filter{}, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy while a scheduled sync runs", err)
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.SyncNow(context.Background(), Filter{}, false); err != nil {
		t.Fatalf("manual sync after the scheduled one finished: %v", err)
	}
}

func TestRunDueAttributesUsesTriggerTimeCapturedBeforeWaitingForLock(t *testing.T) {
	syncer, _, attributes := newTestSyncer(t)
	var clockMu sync.Mutex
	current := time.Date(2026, 9, 25, 3, 0, 45, 0, time.UTC) // 计划 03:00 的回调在 03:00:45 进入
	syncer.Attributes.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return current
	}
	syncer.mu.Lock() // 手动同步占锁
	done := make(chan error, 1)
	go func() { done <- syncer.RunDueAttributes(context.Background()) }()
	time.Sleep(50 * time.Millisecond) // 让回调先固定触发时间并开始等锁
	clockMu.Lock()
	current = time.Date(2026, 9, 25, 3, 1, 30, 0, time.UTC) // 获得锁时已过计划分钟窗口
	clockMu.Unlock()
	syncer.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if attributes.calls != 1 {
		t.Fatalf("attribute calls = %d, want 1: 持锁跨过计划分钟不能漏跑", attributes.calls)
	}
}
