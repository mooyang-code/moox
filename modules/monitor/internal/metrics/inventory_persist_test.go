package metrics

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memInventoryStore struct {
	raw       []byte
	fetchedAt time.Time
}

func (s *memInventoryStore) SaveInventory(_ context.Context, snapshot []byte, fetchedAt time.Time) error {
	s.raw, s.fetchedAt = snapshot, fetchedAt
	return nil
}

func (s *memInventoryStore) LoadInventory(context.Context) ([]byte, time.Time, error) {
	return s.raw, s.fetchedAt, nil
}

type flakyInventoryProvider struct {
	snapshot TaskResultInventorySnapshot
	err      error
}

func (p *flakyInventoryProvider) FetchTaskResultInventory(context.Context) (TaskResultInventorySnapshot, error) {
	return p.snapshot, p.err
}

func TestInventoryCacheSurvivesCollectorOutageAndRestart(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	provider := &flakyInventoryProvider{snapshot: TaskResultInventorySnapshot{ID: "inv-1", ObservedAt: now, Entries: []TaskResultInventoryEntry{{
		SpaceID: "crypto", TaskID: "t", DatasetID: "d", ViewID: "v", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
		ObservedAt: now, AllowedLatenessSeconds: 15,
	}}}}
	store := &memInventoryStore{}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	cache.SetStore(store)
	snapshot, stale, err := cache.GetAllowStale(context.Background(), now)
	if err != nil || stale || snapshot.ID != "inv-1" || len(store.raw) == 0 {
		t.Fatalf("fresh fetch must persist: stale=%v err=%v stored=%d", stale, err, len(store.raw))
	}

	// 采集服务不可用：缓存过期后仍返回旧清单并标记过期。
	provider.err = errors.New("collector unavailable")
	snapshot, stale, err = cache.GetAllowStale(context.Background(), now.Add(10*time.Minute))
	if err != nil || !stale || snapshot.ID != "inv-1" {
		t.Fatalf("stale inventory: stale=%v err=%v id=%s", stale, err, snapshot.ID)
	}

	// Monitor 与采集服务同时重启：新缓存从持久化恢复。
	restarted, _ := NewTaskResultInventoryCache(provider, time.Minute, 10)
	restarted.SetStore(store)
	snapshot, stale, err = restarted.GetAllowStale(context.Background(), now.Add(20*time.Minute))
	if err != nil || !stale || snapshot.ID != "inv-1" || snapshot.Entries[0].AllowedLatenessSeconds != 15 {
		t.Fatalf("restored inventory: stale=%v err=%v snapshot=%+v", stale, err, snapshot)
	}

	// 从未取到过清单：返回错误。
	empty, _ := NewTaskResultInventoryCache(provider, time.Minute, 10)
	empty.SetStore(&memInventoryStore{})
	if _, _, err := empty.GetAllowStale(context.Background(), now); err == nil {
		t.Fatal("an inventory that was never fetched must fail")
	}
}
